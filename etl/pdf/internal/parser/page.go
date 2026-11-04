// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parser

type Page struct {
	V Value
}

func (p Page) findInherited(key string) Value {
	for value := p.V; !value.IsNull(); value = value.Key("Parent") {
		if inherited := value.Key(key); !inherited.IsNull() {
			return inherited
		}
	}
	return Value{}
}

func (p Page) Resources() Value {
	return p.findInherited("Resources")
}

func (p Page) PlainText() (result string, err error) {
	defer recoverTo(&err, func() { result = "" })
	if p.V.IsNull() {
		return "", nil
	}
	content := p.V.Key("Contents")
	if content.IsNull() {
		return "", nil
	}
	resources := p.Resources()
	interpreter := textInterpreter{
		budget:        p.V.r.budget,
		pageResources: resources,
		activeForms:   make(map[objptr]bool),
	}
	interpreter.interpret(content, &textState{resources: resources})
	return interpreter.text.String(), nil
}
