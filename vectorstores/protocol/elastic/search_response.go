package elastic

import (
	"encoding/json"
	"errors"
)

const (
	exactTotalRelation = "eq"
	routingField       = "_routing"
)

type SearchResponse struct {
	ScrollID        string        `json:"_scroll_id"`
	TerminatedEarly *bool         `json:"terminated_early"`
	TimedOut        *bool         `json:"timed_out"`
	Shards          *SearchShards `json:"_shards"`
	Hits            *SearchHits   `json:"hits"`
}

func (s SearchResponse) Validate(index string) error {
	if s.TerminatedEarly != nil && *s.TerminatedEarly || s.TimedOut == nil || *s.TimedOut || s.Shards == nil || s.Shards.Total == nil || s.Shards.Successful == nil || s.Shards.Failed == nil || *s.Shards.Total <= 0 || *s.Shards.Failed != 0 || *s.Shards.Successful != *s.Shards.Total {
		return errors.New("elastic: native search is incomplete or lacks shard acknowledgments")
	}
	if s.Hits == nil || s.Hits.Hits == nil || s.Hits.Total == nil || s.Hits.Total.Value == nil || *s.Hits.Total.Value < 0 || s.Hits.Total.Relation != exactTotalRelation {
		return errors.New("elastic: native search lacks complete hits or an exact total")
	}
	for _, hit := range *s.Hits.Hits {
		if hit.Index != index {
			return errors.New("elastic: native hit is outside the concrete index")
		}
	}
	return nil
}

type SearchShards struct {
	Total      *int `json:"total"`
	Successful *int `json:"successful"`
	Failed     *int `json:"failed"`
}

type SearchHits struct {
	Total *SearchTotal `json:"total"`
	Hits  *[]Hit       `json:"hits"`
}

type SearchTotal struct {
	Value    *int64 `json:"value"`
	Relation string `json:"relation"`
}

type Hit struct {
	Index       string                       `json:"_index"`
	ID          string                       `json:"_id"`
	Routing     string                       `json:"_routing"`
	Fields      map[string][]json.RawMessage `json:"fields"`
	SeqNo       *int64                       `json:"_seq_no"`
	PrimaryTerm *int64                       `json:"_primary_term"`
	Score       *float64                     `json:"_score"`
	Source      json.RawMessage              `json:"_source"`
}

func (h Hit) Validate() error {
	if h.Index == "" || h.Routing != "" || len(h.Fields[routingField]) != 0 {
		return errors.New("elastic: native hit has no concrete index or uses custom routing")
	}
	if err := DocumentID(h.ID).Validate(); err != nil {
		return err
	}
	if _, err := h.Version(); err != nil {
		return err
	}
	if len(h.Source) == 0 {
		return errors.New("elastic: native hit is missing stored _source")
	}
	return nil
}

func (h Hit) Version() (Version, error) {
	if h.SeqNo == nil || h.PrimaryTerm == nil {
		return Version{}, errors.New("elastic: native hit is missing concurrency tokens")
	}
	version := Version{SeqNo: *h.SeqNo, PrimaryTerm: *h.PrimaryTerm}
	return version, version.Validate()
}
