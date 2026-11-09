package elastic

import "errors"

type Version struct {
	SeqNo       int64
	PrimaryTerm int64
}

func (v Version) Validate() error {
	if v.SeqNo < 0 || v.PrimaryTerm <= 0 {
		return errors.New("elastic: invalid conditional document version")
	}
	return nil
}
