package speech

import (
	"errors"
	"iter"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
)

func TestCollectLimitAndFailureDiscardPartialOutput(t *testing.T) {
	fault := errors.New("tail failed")
	for _, sample := range []struct {
		name    string
		audio   []string
		limit   int64
		tail    error
		want    string
		failure error
	}{
		{name: "exact", audio: []string{"ab", "cd"}, limit: 4, want: "abcd"},
		{name: "single chunk too large", audio: []string{"abcde"}, limit: 4, failure: ErrInvalidResponse},
		{name: "aggregate too large", audio: []string{"abc", "de"}, limit: 4, failure: ErrInvalidResponse},
		{name: "tail failure", audio: []string{"ab"}, limit: 4, tail: fault, failure: fault},
		{name: "empty", limit: 4, failure: ErrInvalidResponse},
		{name: "invalid chunk", audio: []string{""}, limit: 4, failure: ErrInvalidResponse},
		{name: "invalid limit", limit: 0, failure: ErrInvalidOptions},
	} {
		t.Run(sample.name, func(t *testing.T) {
			stopped := false
			stream := func(yield func(*Response, error) bool) {
				defer func() { stopped = true }()
				for _, audio := range sample.audio {
					if !yield(&Response{Output: &Output{MIME: "audio/mpeg", Audio: []byte(audio)}}, nil) {
						return
					}
				}
				if sample.tail != nil {
					yield(nil, sample.tail)
				}
			}
			response, err := Collect(stream, sample.limit)
			if !errors.Is(err, sample.failure) {
				t.Fatalf("response=%v error=%v", response, err)
			}
			if sample.failure != nil {
				if response != nil {
					t.Fatal("partial response returned")
				}
			} else if string(response.Output.Audio) != sample.want {
				t.Fatalf("audio=%q", response.Output.Audio)
			}
			if stopped != (sample.limit > 0) {
				t.Fatalf("stopped=%v", stopped)
			}
		})
	}
	if response, err := Collect(nil, 1); response != nil || !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("response=%v error=%v", response, err)
	}
	if response, err := Collect(func(yield func(*Response, error) bool) { yield(nil, nil) }, 1); response != nil || !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("response=%v error=%v", response, err)
	}
}

func TestCollectSnapshotsTerminalMetadata(t *testing.T) {
	outputMetadata := metadata.Map{}
	if err := outputMetadata.Set("format", "mp3"); err != nil {
		t.Fatal(err)
	}
	responseMetadata := metadata.Map{}
	if err := responseMetadata.Set("request_id", "terminal"); err != nil {
		t.Fatal(err)
	}
	last := &Response{Output: &Output{MIME: "audio/mpeg", Audio: []byte("b"), Metadata: outputMetadata}, Metadata: &ResponseMetadata{Model: "model", Extra: responseMetadata}}
	stream := iter.Seq2[*Response, error](func(yield func(*Response, error) bool) {
		if yield(&Response{Output: &Output{MIME: "audio/mpeg", Audio: []byte("a")}}, nil) {
			yield(last, nil)
		}
		last.Output.Audio[0] = 'z'
		last.Metadata.Model = "changed"
		if err := last.Output.Metadata.Set("format", "wav"); err != nil {
			t.Fatal(err)
		}
		if err := last.Metadata.Extra.Set("request_id", "changed"); err != nil {
			t.Fatal(err)
		}
		last.Output, last.Metadata = nil, nil
	})
	response, err := Collect(stream, 2)
	if err != nil {
		t.Fatal(err)
	}
	format, _, err := response.Output.Metadata.Decode[string]("format")
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := response.Metadata.Extra.Decode[string]("request_id")
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Output.Audio) != "ab" || response.Metadata.Model != "model" || format != "mp3" || id != "terminal" {
		t.Fatalf("audio=%q model=%q format=%q id=%q", response.Output.Audio, response.Metadata.Model, format, id)
	}
}

func TestCollectRejectsAudioThatChangesEncoding(t *testing.T) {
	_, err := Collect(func(yield func(*Response, error) bool) {
		_ = yield(&Response{Output: &Output{MIME: "audio/mpeg", Audio: []byte("a")}}, nil) &&
			yield(&Response{Output: &Output{MIME: "audio/wav", Audio: []byte("b")}}, nil)
	}, DefaultMaxAudioBytes)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("Collect = %v, want ErrInvalidResponse", err)
	}
	response, err := Collect(func(yield func(*Response, error) bool) {
		yield(&Response{Output: &Output{MIME: "audio/L16;rate=24000", Audio: []byte("pcm")}}, nil)
	}, DefaultMaxAudioBytes)
	if err != nil || response.Output.MIME != "audio/L16;rate=24000" {
		t.Fatalf("Collect = %#v, %v", response, err)
	}
}
