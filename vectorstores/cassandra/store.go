package cassandra

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "Cassandra"
const (
	DefaultKeyspaceName     = "scope"
	DefaultTableName        = "vector_store"
	DefaultIDColumn         = "id"
	DefaultContentColumn    = "content"
	DefaultMetadataColumn   = "metadata"
	DefaultEmbeddingColumn  = "embedding"
	DefaultSimilarity       = SimilarityCosine
	paxosTableBindingPrefix = "scope_cassandra_paxos:"
)

// Session supplies native queries. The host owns the Apache driver's session.
type Session interface {
	Query(string, ...any) *gocql.Query
}

// SimilarityFunction selects a native Cassandra scalar function. No vector
// index participates in selecting or ranking Core matches.
type SimilarityFunction string

const (
	SimilarityCosine     SimilarityFunction = "cosine"
	SimilarityDotProduct SimilarityFunction = "dot_product"
	SimilarityEuclidean  SimilarityFunction = "euclidean"
)

func (s SimilarityFunction) Valid() bool {
	return s == SimilarityCosine || s == SimilarityDotProduct || s == SimilarityEuclidean
}
func (s SimilarityFunction) String() string { return string(s) }
func (s SimilarityFunction) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, fmt.Errorf("cassandra: %w: non-finite native similarity", vectorstore.ErrInvalidScore)
	}
	score := vectorstore.Score(raw)
	if s == SimilarityDotProduct {
		score = vectorstore.ScoreFromInnerProduct(2*raw - 1)
	}
	if err := score.Validate(); err != nil {
		return 0, err
	}
	return score, nil
}

// StoreConfig selects an exclusively owned table whose document mutations all
// use this adapter's Paxos protocol. Its native binding rejects the former
// ordinary-write protocol; those tables must be recreated, because their
// timestamps can override conditional mutations.
// Empty names use exported defaults. Session and Space are required.
type StoreConfig struct {
	Session         Session
	KeyspaceName    string
	TableName       string
	IDColumn        string
	ContentColumn   string
	EmbeddingColumn string
	MetadataColumn  string
	Space           vectorstore.SpaceID
	Similarity      SimilarityFunction
	// CreateDimensions seeds a new vector column when InitializeSchema is true.
	// Existing native schema always owns the width used by operations.
	CreateDimensions int
	// InitializeSchema creates only the keyspace and four-column table. Every
	// constructor verifies the actual native column types and primary key.
	InitializeSchema bool
	// KeyspaceReplication is the trusted CQL replication clause for creation.
	// Empty creates a single-replica SimpleStrategy keyspace.
	KeyspaceReplication string
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if lo.IsNil(s.Session) {
		return errors.New("cassandra: Session is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if !s.Similarity.Valid() {
		return fmt.Errorf("cassandra: unsupported Similarity %q", s.Similarity)
	}
	if s.InitializeSchema && s.CreateDimensions <= 0 {
		return errors.New("cassandra: CreateDimensions must be positive when InitializeSchema is enabled")
	}
	if !s.InitializeSchema && s.CreateDimensions != 0 {
		return errors.New("cassandra: CreateDimensions requires InitializeSchema")
	}
	return s.validateIdentifiers()
}
func (s StoreConfig) validateIdentifiers() error {
	if err := identifier(s.KeyspaceName).validate("KeyspaceName"); err != nil {
		return err
	}
	if err := identifier(s.TableName).validate("TableName"); err != nil {
		return err
	}
	fields := []struct{ name, value string }{{"IDColumn", s.IDColumn}, {"ContentColumn", s.ContentColumn}, {"EmbeddingColumn", s.EmbeddingColumn}, {"MetadataColumn", s.MetadataColumn}}
	seen := make(map[string]string, len(fields))
	for _, field := range fields {
		if err := identifier(field.value).validate(field.name); err != nil {
			return err
		}
		if owner, exists := seen[field.value]; exists {
			return fmt.Errorf("cassandra: %s and %s both use column %q", owner, field.name, field.value)
		}
		seen[field.value] = field.name
	}
	return nil
}
func (s *StoreConfig) applyDefaults() {
	s.KeyspaceName = cmp.Or(s.KeyspaceName, DefaultKeyspaceName)
	s.TableName = cmp.Or(s.TableName, DefaultTableName)
	s.IDColumn = cmp.Or(s.IDColumn, DefaultIDColumn)
	s.ContentColumn = cmp.Or(s.ContentColumn, DefaultContentColumn)
	s.EmbeddingColumn = cmp.Or(s.EmbeddingColumn, DefaultEmbeddingColumn)
	s.MetadataColumn = cmp.Or(s.MetadataColumn, DefaultMetadataColumn)
	s.Similarity = cmp.Or(s.Similarity, DefaultSimilarity)
	s.KeyspaceReplication = cmp.Or(s.KeyspaceReplication, "{'class': 'SimpleStrategy', 'replication_factor': 1}")
}

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

// Store uses Core JSON and filter semantics, native vector codecs and scalar
// similarities, and one Paxos protocol for every document mutation. Conditional
// deletion protects the exact rows observed by a scan.
type Store struct {
	session         Session
	fullTable       string
	idColumn        string
	contentColumn   string
	embeddingColumn string
	metadataColumn  string
	space           vectorstore.SpaceID
	dimensions      int
	similarity      SimilarityFunction
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	store := &Store{session: config.Session, fullTable: quoteIdentifier(config.KeyspaceName) + "." + quoteIdentifier(config.TableName), idColumn: config.IDColumn, contentColumn: config.ContentColumn, embeddingColumn: config.EmbeddingColumn, metadataColumn: config.MetadataColumn, space: config.Space, similarity: config.Similarity}
	if config.InitializeSchema {
		statements := []string{
			fmt.Sprintf("CREATE KEYSPACE IF NOT EXISTS %s WITH REPLICATION = %s", quoteIdentifier(config.KeyspaceName), config.KeyspaceReplication),
			fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s text PRIMARY KEY, %s text, %s text, %s vector<float,%d>) WITH comment = '%s'", store.fullTable, quoteIdentifier(store.idColumn), quoteIdentifier(store.contentColumn), quoteIdentifier(store.metadataColumn), quoteIdentifier(store.embeddingColumn), config.CreateDimensions, store.tableBinding()),
		}
		for _, statement := range statements {
			if err := store.session.Query(statement).ExecContext(ctx); err != nil {
				return nil, fmt.Errorf("cassandra: create schema: %w", err)
			}
		}
	}
	if err := store.loadSchema(ctx, config.KeyspaceName, config.TableName); err != nil {
		return nil, fmt.Errorf("cassandra: verify schema: %w", err)
	}
	return store, nil
}
func (s *Store) tableBinding() string {
	return paxosTableBindingPrefix + s.space.StorageKey()
}
func (s *Store) loadSchema(ctx context.Context, keyspace, table string) error {
	var binding string
	if err := s.session.Query("SELECT comment FROM system_schema.tables WHERE keyspace_name=? AND table_name=?", keyspace, table).ScanContext(ctx, &binding); err != nil {
		return err
	}
	if binding != s.tableBinding() {
		return fmt.Errorf("cassandra: table must declare the current Paxos-only space binding: %w", vectorstore.ErrIncompatibleSpace)
	}
	iter := s.session.Query("SELECT * FROM " + s.fullTable + " LIMIT 1").IterContext(ctx)
	columns := iter.Columns()
	closeErr := iter.Close()
	if closeErr != nil {
		return closeErr
	}
	if len(columns) != 4 {
		return errors.New("cassandra: table must contain exactly four current document columns")
	}
	for _, column := range columns {
		switch column.Name {
		case s.idColumn, s.contentColumn, s.metadataColumn:
			if column.TypeInfo.Type() != gocql.TypeVarchar && column.TypeInfo.Type() != gocql.TypeText {
				return fmt.Errorf("cassandra: column %q must be text", column.Name)
			}
		case s.embeddingColumn:
			nativeType, ok := column.TypeInfo.(gocql.VectorType)
			if !ok || nativeType.SubType.Type() != gocql.TypeFloat || nativeType.Dimensions <= 0 {
				return errors.New("cassandra: embedding must be vector<float,N>")
			}
			s.dimensions = nativeType.Dimensions
		default:
			return fmt.Errorf("cassandra: unexpected column %q", column.Name)
		}
	}
	if s.dimensions == 0 {
		return errors.New("cassandra: missing embedding column")
	}
	if err := s.verifyPrimaryKey(ctx, keyspace, table); err != nil {
		return err
	}
	probe := make([]float32, s.dimensions)
	probe[0] = 1
	_, err := s.nativeSimilarity(ctx, probe, probe)
	return err
}
func (s *Store) verifyPrimaryKey(ctx context.Context, keyspace, table string) (err error) {
	iter := s.session.Query("SELECT column_name,kind FROM system_schema.columns WHERE keyspace_name=? AND table_name=?", keyspace, table).IterContext(ctx)
	defer func() { err = errors.Join(err, iter.Close()) }()
	var name, kind string
	primaryKeys := 0
	for iter.Scan(&name, &kind) {
		if kind == "partition_key" || kind == "clustering" {
			if name != s.idColumn || kind != "partition_key" {
				return errors.New("cassandra: document ID must be the sole primary key")
			}
			primaryKeys++
		}
	}
	if primaryKeys != 1 {
		return errors.New("cassandra: document ID must be the sole primary key")
	}
	return nil
}

// storedRecord is the captured native row, including the exact metadata string
// used by CAS. Re-encoding that string would change the conditional write token.
type storedRecord struct {
	id       string
	content  string
	metadata string
	vector   []float32
}

func encodeStoredRecord(doc *document.Document, vector []float64) (storedRecord, error) {
	encoded, err := doc.MarshalStoredMetadata()
	if err != nil {
		return storedRecord{}, err
	}
	narrowed := corevector.Float32(vector)
	if err := corevector.Coordinates[float32](narrowed).Validate(); err != nil {
		return storedRecord{}, fmt.Errorf("%w: %w", vectorstore.ErrInvalidRequest, err)
	}
	return storedRecord{id: doc.ID, content: doc.Text, metadata: string(encoded), vector: narrowed}, nil
}
func decodeStoredRecord(record storedRecord) (*document.Document, error) {
	doc := new(document.Document)
	if err := doc.UnmarshalStoredMetadata([]byte(record.metadata)); err != nil {
		return nil, fmt.Errorf("cassandra: decode metadata for %q: %w", record.id, err)
	}
	doc.ID, doc.Text = record.id, record.content
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(doc.ID) == "" {
		return nil, vectorstore.ErrMissingDocumentID
	}
	if err := corevector.Coordinates[float32](record.vector).Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", vectorstore.ErrInvalidRequest, err)
	}
	return doc, nil
}

type storedCandidate struct {
	record   storedRecord
	document *document.Document
}
type rankedResult struct {
	result           *vectorstore.SearchResult
	nativeSimilarity float64
}

func (s *Store) nativeSimilarity(ctx context.Context, left, right []float32) (float64, error) {
	query := fmt.Sprintf("SELECT similarity_%s((vector<float,%d>)?, (vector<float,%d>)?) FROM system.local WHERE key='local'", s.similarity, s.dimensions, s.dimensions)
	var value float32
	if err := s.session.Query(query, left, right).ScanContext(ctx, &value); err != nil {
		return 0, fmt.Errorf("cassandra: native similarity: %w", err)
	}
	raw := float64(value)
	if _, err := s.similarity.score(raw); err != nil {
		return 0, err
	}
	return raw, nil
}
func (s *Store) validateNativeVector(ctx context.Context, vector []float32) error {
	// Cosine rejects zero vectors. For dot product, self-similarity can overflow
	// despite finite components, so a zero operand validates the vector instead.
	right := make([]float32, s.dimensions)
	if s.similarity == SimilarityCosine {
		right = vector
	}
	_, err := s.nativeSimilarity(ctx, vector, right)
	return err
}
func (s *Store) Upsert(ctx context.Context, request *vectorstore.Batch) error {
	if err := request.ValidateFor(s.space); err != nil {
		return err
	}
	records := make([]storedRecord, 0, len(request.Records))
	ids := make([]string, 0, len(request.Records))
	for _, r := range request.Records {
		if r.Document.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		if len(r.Values) != s.dimensions {
			return fmt.Errorf("cassandra: expected %d coordinates: %w", s.dimensions, vectorstore.ErrInvalidRequest)
		}
		row, err := encodeStoredRecord(r.Document, r.Values)
		if err != nil {
			return err
		}
		records = append(records, row)
		ids = append(ids, r.Document.ID)
	}
	for _, record := range records {
		if err := s.validateNativeVector(ctx, record.vector); err != nil {
			return err
		}
	}
	for i, record := range records {
		if err := ctx.Err(); err != nil {
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Acknowledged: ids[:i], Unattempted: ids[i:]})
		}
		applied, err := s.upsertRecord(ctx, record)
		if err != nil {
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Acknowledged: ids[:i], Unconfirmed: ids[i : i+1], Unattempted: ids[i+1:]})
		}
		if !applied {
			err := fmt.Errorf("cassandra: document %q was deleted during upsert", record.id)
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Acknowledged: ids[:i], Rejected: ids[i : i+1], Unattempted: ids[i+1:]})
		}
	}
	return nil
}
func (s *Store) upsertRecord(ctx context.Context, record storedRecord) (bool, error) {
	insert := fmt.Sprintf("INSERT INTO %s (%s,%s,%s,%s) VALUES (?,?,?,?) IF NOT EXISTS", s.fullTable, quoteIdentifier(s.idColumn), quoteIdentifier(s.contentColumn), quoteIdentifier(s.metadataColumn), quoteIdentifier(s.embeddingColumn))
	applied, err := s.applyConditional(ctx, insert, record.id, record.content, record.metadata, record.vector)
	if err != nil || applied {
		return applied, err
	}
	// A concurrent deletion between these two proposals is a known rejection,
	// not permission to retry and recreate a row the caller did not observe.
	update := fmt.Sprintf("UPDATE %s SET %s=?,%s=?,%s=? WHERE %s=? IF EXISTS", s.fullTable, quoteIdentifier(s.contentColumn), quoteIdentifier(s.metadataColumn), quoteIdentifier(s.embeddingColumn), quoteIdentifier(s.idColumn))
	return s.applyConditional(ctx, update, record.content, record.metadata, record.vector, record.id)
}
func (s *Store) applyConditional(ctx context.Context, statement string, values ...any) (bool, error) {
	// Mixing ordinary timestamps with Paxos can acknowledge a deletion that
	// loses to an older write. All row mutations use the same quorum and clock.
	return s.session.Query(statement, values...).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).DefaultTimestamp(false).MapScanCASContext(ctx, map[string]any{})
}
func (s *Store) selectCandidates(meter *vectorstore.ScanMeter, predicate filter.Predicate) (selected []storedCandidate, err error) {
	ctx := meter.Context()
	query := fmt.Sprintf("SELECT %s,%s,%s,%s FROM %s", quoteIdentifier(s.idColumn), quoteIdentifier(s.contentColumn), quoteIdentifier(s.metadataColumn), quoteIdentifier(s.embeddingColumn), s.fullTable)
	iter := s.session.Query(query).Consistency(gocql.Quorum).PageSize(1).IterContext(ctx)
	defer func() {
		err = errors.Join(err, iter.Close())
		if err != nil {
			selected = nil
		}
	}()
	for {
		var record storedRecord
		if !iter.Scan(&record.id, &record.content, &record.metadata, &record.vector) {
			break
		}
		if err := meter.Charge(int64(len(record.id)) + int64(len(record.content)) + int64(len(record.metadata)) + 4*int64(len(record.vector))); err != nil {
			return nil, err
		}
		doc, decodeErr := decodeStoredRecord(record)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if vectorErr := s.validateNativeVector(ctx, record.vector); vectorErr != nil {
			return nil, vectorErr
		}
		if predicate != nil {
			values, valueErr := doc.Metadata.Values()
			if valueErr != nil {
				return nil, valueErr
			}
			match, matchErr := filter.Match(predicate, values)
			if matchErr != nil {
				return nil, fmt.Errorf("cassandra: filter document %q: %w", doc.ID, matchErr)
			}
			if !match {
				continue
			}
		}
		selected = append(selected, storedCandidate{record: record, document: doc})
	}
	return selected, nil
}
func (s *Store) QueryByScan(ctx context.Context, request *vectorstore.Query, budget vectorstore.ScanBudget) (response *vectorstore.SearchResponse, err error) {
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	if len(request.Values) != s.dimensions {
		return nil, vectorstore.ErrInvalidRequest
	}
	queryVector := corevector.Float32(request.Values)
	if err = corevector.Coordinates[float32](queryVector).Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", vectorstore.ErrInvalidRequest, err)
	}
	meter, err := budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	ctx = meter.Context()
	if err = s.validateNativeVector(ctx, queryVector); err != nil {
		return nil, err
	}
	candidates, err := s.selectCandidates(meter, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	ranked := make([]rankedResult, 0, len(candidates))
	for _, candidate := range candidates {
		raw, scoreErr := s.nativeSimilarity(ctx, candidate.record.vector, queryVector)
		if scoreErr != nil {
			return nil, scoreErr
		}
		score, scoreErr := s.similarity.score(raw)
		if scoreErr != nil {
			return nil, scoreErr
		}
		result, resultErr := vectorstore.NewSearchResult(candidate.document, score)
		if resultErr != nil {
			return nil, resultErr
		}
		if score >= request.Options.MinScore {
			ranked = append(ranked, rankedResult{result: result, nativeSimilarity: raw})
		}
	}
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		if order := cmp.Compare(right.nativeSimilarity, left.nativeSimilarity); order != 0 {
			return order
		}
		return strings.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	limit := min(len(ranked), request.Options.ResultLimit())
	results := make([]*vectorstore.SearchResult, limit)
	for i := range results {
		results[i] = ranked[i].result
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}
func (s *Store) DeleteMatching(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	meter, err := budget.Begin(ctx)
	if err != nil {
		return err
	}
	defer meter.Close()
	ctx = meter.Context()
	candidates, err := s.selectCandidates(meter, predicate)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("DELETE FROM %s WHERE %s=? IF %s=? AND %s=? AND %s=?", s.fullTable, quoteIdentifier(s.idColumn), quoteIdentifier(s.contentColumn), quoteIdentifier(s.metadataColumn), quoteIdentifier(s.embeddingColumn))
	for _, candidate := range candidates {
		record := candidate.record
		applied, casErr := s.applyConditional(ctx, query, record.id, record.content, record.metadata, record.vector)
		if casErr != nil {
			return fmt.Errorf("cassandra: delete %q: %w", record.id, casErr)
		}
		if !applied {
			return fmt.Errorf("cassandra: document %q changed during filter deletion", record.id)
		}
	}
	return nil
}
func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	query := fmt.Sprintf("DELETE FROM %s WHERE %s=? IF EXISTS", s.fullTable, quoteIdentifier(s.idColumn))
	for _, id := range ids {
		if _, err := s.applyConditional(ctx, query, id); err != nil {
			return fmt.Errorf("cassandra: delete %q: %w", id, err)
		}
	}
	return nil
}
