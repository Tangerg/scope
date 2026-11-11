// Package elastictest serves one Elastic-protocol index from memory. Store
// behavior and every engine adapter are tested against the same native
// exchange; an engine contributes only what differs between engines: its
// embedding mapping, setting defaults and nearest-neighbor clause.
package elastictest

import (
	"bytes"
	"cmp"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
)

// IndexName names the one index an Index serves.
const IndexName = "documents"

const (
	defaultScore    = 0.8
	defaultPageSize = 2
	seqNo           = 3
	primaryTerm     = 1
)

// Engine is the part of the native exchange that differs between engines.
type Engine struct {
	// Embedding is the mapping of the embedding field.
	Embedding any
	// Defaults are the index setting defaults the engine reads.
	Defaults map[string]string
	// Neighbors reads the nearest-neighbor clause of a search body. A body
	// without one is a scan, and a clause without an ID filter restricts
	// nothing, so it returns nil IDs.
	Neighbors func(body []byte) (ids []string, nearest bool, err error)
	// Respond answers a request outside the protocol, such as the probes a
	// native client sends before its first operation.
	Respond func(*http.Request) (response any, handled bool)
	// Header holds response headers the engine's client verifies.
	Header http.Header
}

// Mapping is the native mapping an Index reports. Tests change it to model an
// index Store must refuse.
type Mapping struct {
	Meta struct {
		Space string `json:"scope_vector_space"`
	} `json:"_meta"`
	Dynamic    string         `json:"dynamic"`
	Properties map[string]any `json:"properties"`
	Source     Source         `json:"_source"`
	Routing    struct {
		Required bool `json:"required"`
	} `json:"_routing"`
	Runtime          map[string]any `json:"runtime"`
	Derived          map[string]any `json:"derived"`
	DynamicTemplates []any          `json:"dynamic_templates"`
}

// Field maps a record field other than the embedding.
type Field struct {
	Type      string `json:"type"`
	Index     *bool  `json:"index,omitzero"`
	DocValues *bool  `json:"doc_values,omitzero"`
}

type Source struct {
	Enabled  *bool    `json:"enabled,omitzero"`
	Mode     string   `json:"mode,omitzero"`
	Includes []string `json:"includes,omitzero"`
	Excludes []string `json:"excludes,omitzero"`
}

// Index is one in-memory index. Its exported fields are the native state a
// test observes and the faults it injects. It serves one request at a time.
type Index struct {
	Mapping  Mapping
	Settings elastic.IndexSettings
	Rows     map[string]elastic.StoredDocument
	// Scores override the native score of a row, which is otherwise 0.8.
	Scores map[string]float64
	// PageSize bounds every scan page, so a scan crosses its cursor.
	PageSize int
	// Writes counts indexed records and Cleared counts released cursors.
	Writes, Cleared int
	// Groups holds the candidate count of each nearest-neighbor search; an
	// unrestricted search records zero.
	Groups []int
	// SearchBodies and BulkBodies hold every search and bulk request body.
	SearchBodies, BulkBodies [][]byte
	// BulkFailureAt fails the bulk request with this one-based ordinal.
	BulkFailureAt int
	// Reply answers every nearest-neighbor search and ScanReply every scan
	// start in place of the rows.
	Reply, ScanReply any
	// BeforeSearch runs once before the next nearest-neighbor search, and
	// BeforeDelete once before the next delete, which then conflicts.
	BeforeSearch, BeforeDelete func()

	t        testing.TB
	engine   Engine
	snapshot []elastic.Hit
	position int
}

// NewIndex serves an empty index in space whose record layout Store admits.
func NewIndex(t testing.TB, space vectorstore.SpaceID, engine Engine) *Index {
	t.Helper()
	settings := elastic.IndexSettings{Settings: map[string]string{}, Defaults: map[string]string{"index.max_result_window": "10000"}}
	maps.Copy(settings.Defaults, engine.Defaults)
	index := &Index{
		Settings: settings,
		Rows:     make(map[string]elastic.StoredDocument),
		Scores:   make(map[string]float64),
		PageSize: defaultPageSize,
		t:        t,
		engine:   engine,
	}
	index.Mapping.Meta.Space = string(space)
	index.Mapping.Dynamic = "strict"
	index.Mapping.Properties = map[string]any{
		elastic.ContentField:   Field{Type: "text"},
		elastic.MetadataField:  Field{Type: "keyword", Index: new(false), DocValues: new(false)},
		elastic.EmbeddingField: engine.Embedding,
	}
	return index
}

// Perform answers request in process, for a Store built over a Perform port.
func (i *Index) Perform(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	i.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

// Hit is the native hit of the row with id.
func (i *Index) Hit(id string) elastic.Hit {
	source, err := jsonv2.Marshal(i.Rows[id])
	if err != nil {
		i.t.Error(err)
	}
	score, scored := i.Scores[id]
	if !scored {
		score = defaultScore
	}
	return elastic.Hit{Index: IndexName, ID: id, SeqNo: new(int64(seqNo)), PrimaryTerm: new(int64(primaryTerm)), Score: &score, Source: source}
}

// Page is one complete search page of hits out of total.
func (i *Index) Page(hits []elastic.Hit, total int, cursor string) elastic.SearchResponse {
	if hits == nil {
		hits = []elastic.Hit{}
	}
	return elastic.SearchResponse{
		ScrollID: cursor, TimedOut: new(false),
		Shards: &elastic.SearchShards{Total: new(1), Successful: new(1), Failed: new(0)},
		Hits:   &elastic.SearchHits{Hits: &hits, Total: &elastic.SearchTotal{Value: new(int64(total)), Relation: "eq"}},
	}
}

func (i *Index) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	maps.Copy(w.Header(), i.engine.Header)
	w.Header().Set("Content-Type", "application/json")
	response, handled, status := i.respond(r)
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	if !handled {
		i.t.Errorf("elastictest: unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := jsonv2.MarshalWrite(w, response); err != nil {
		i.t.Error(err)
	}
}

func (i *Index) respond(r *http.Request) (response any, handled bool, status int) {
	if i.engine.Respond != nil {
		if response, handled := i.engine.Respond(r); handled {
			return response, true, http.StatusOK
		}
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/_mapping"):
		return map[string]map[string]Mapping{IndexName: {"mappings": i.Mapping}}, true, http.StatusOK
	case strings.HasSuffix(r.URL.Path, "/_settings"):
		return map[string]elastic.IndexSettings{IndexName: i.Settings}, true, http.StatusOK
	case r.Method == http.MethodDelete && r.URL.Path == "/_search/scroll":
		i.Cleared++
		return map[string]any{"succeeded": true, "num_freed": 1}, true, http.StatusOK
	case r.URL.Path == "/_bulk":
		return i.bulk(r)
	case r.URL.Path == "/_search/scroll":
		end := min(i.position+i.PageSize, len(i.snapshot))
		page := i.Page(i.snapshot[i.position:end], len(i.snapshot), "cursor")
		i.position = end
		return page, true, http.StatusOK
	case strings.HasSuffix(r.URL.Path, "/_search"):
		return i.search(r)
	default:
		return nil, false, http.StatusOK
	}
}

// bulkAction is one action line of a bulk request; an index action is
// followed by its record line.
type bulkAction struct {
	Index  *bulkTarget `json:"index,omitzero"`
	Delete *bulkTarget `json:"delete,omitzero"`
}

type bulkTarget struct {
	Index       string `json:"_index"`
	ID          string `json:"_id"`
	SeqNo       *int64 `json:"if_seq_no,omitzero"`
	PrimaryTerm *int64 `json:"if_primary_term,omitzero"`
}

type bulkItem struct {
	Index  string          `json:"_index"`
	ID     string          `json:"_id"`
	Status int             `json:"status"`
	Error  json.RawMessage `json:"error,omitzero"`
}

func (i *Index) bulk(r *http.Request) (any, bool, int) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		i.t.Error(err)
		return nil, true, http.StatusInternalServerError
	}
	i.BulkBodies = append(i.BulkBodies, body)
	if len(i.BulkBodies) == i.BulkFailureAt {
		return nil, true, http.StatusServiceUnavailable
	}
	lines := bytes.Split(bytes.TrimSuffix(body, []byte{'\n'}), []byte{'\n'})
	failed := false
	items := []map[string]bulkItem{}
	for line := 0; line < len(lines); line++ {
		var action bulkAction
		if err := jsonv2.Unmarshal(lines[line], &action, jsonv2.RejectUnknownMembers(true)); err != nil {
			i.t.Error(err)
			return nil, true, http.StatusInternalServerError
		}
		if action.Index != nil {
			line++
			if line >= len(lines) {
				i.t.Error("elastictest: index action without its record")
				return nil, true, http.StatusInternalServerError
			}
			var record elastic.StoredDocument
			if err := jsonv2.Unmarshal(lines[line], &record, jsonv2.RejectUnknownMembers(true)); err != nil {
				i.t.Error(err)
				return nil, true, http.StatusInternalServerError
			}
			i.Rows[action.Index.ID] = record
			i.Writes++
			items = append(items, map[string]bulkItem{"index": {Index: action.Index.Index, ID: action.Index.ID, Status: http.StatusCreated}})
			continue
		}
		item := i.delete(action.Delete)
		failed = failed || item.Status == http.StatusConflict
		items = append(items, map[string]bulkItem{"delete": item})
	}
	return map[string]any{"errors": failed, "items": items}, true, http.StatusOK
}

func (i *Index) delete(target *bulkTarget) bulkItem {
	item := bulkItem{Index: target.Index, ID: target.ID, Status: http.StatusOK}
	if i.BeforeDelete != nil {
		i.BeforeDelete()
		i.BeforeDelete = nil
		item.Status = http.StatusConflict
		return item
	}
	if _, exists := i.Rows[target.ID]; !exists {
		item.Status = http.StatusNotFound
	}
	delete(i.Rows, target.ID)
	return item
}

func (i *Index) search(r *http.Request) (any, bool, int) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		i.t.Error(err)
		return nil, true, http.StatusInternalServerError
	}
	i.SearchBodies = append(i.SearchBodies, body)
	var request struct {
		Size int `json:"size"`
	}
	if decodeErr := jsonv2.Unmarshal(body, &request); decodeErr != nil {
		i.t.Error(decodeErr)
		return nil, true, http.StatusInternalServerError
	}
	ids, nearest, err := i.engine.Neighbors(body)
	if err != nil {
		i.t.Error(err)
		return nil, true, http.StatusInternalServerError
	}
	if !nearest {
		return i.scan(), true, http.StatusOK
	}
	if i.BeforeSearch != nil {
		i.BeforeSearch()
		i.BeforeSearch = nil
	}
	if i.Reply != nil {
		return i.Reply, true, http.StatusOK
	}
	i.Groups = append(i.Groups, len(ids))
	var hits []elastic.Hit
	for id := range i.Rows {
		if ids == nil || slices.Contains(ids, id) {
			hits = append(hits, i.Hit(id))
		}
	}
	slices.SortFunc(hits, func(left, right elastic.Hit) int {
		if order := cmp.Compare(*right.Score, *left.Score); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	hits = hits[:min(len(hits), request.Size)]
	return i.Page(hits, len(hits), ""), true, http.StatusOK
}

// scan starts a scroll over a sorted snapshot of the rows.
func (i *Index) scan() any {
	if i.ScanReply != nil {
		return i.ScanReply
	}
	i.snapshot = nil
	for _, id := range slices.Sorted(maps.Keys(i.Rows)) {
		i.snapshot = append(i.snapshot, i.Hit(id))
	}
	end := min(i.PageSize, len(i.snapshot))
	i.position = end
	return i.Page(i.snapshot[:end], len(i.snapshot), "cursor")
}
