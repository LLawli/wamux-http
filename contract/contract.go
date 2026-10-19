// Package contract embeds the pinned WAHA OpenAPI document: the list of
// operations the edge answers, and the schemas its contract tests check
// responses against.
package contract

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

//go:embed waha-openapi.json
var OpenAPI []byte

// Operation is one method on one path.
type Operation struct {
	Method, Path, Tag, Summary string
}

var (
	once sync.Once
	ops  []Operation
)

// Operations is every operation in the document, sorted by path then method.
func Operations() []Operation {
	once.Do(func() {
		var doc struct {
			Paths map[string]map[string]struct {
				Tags    []string `json:"tags"`
				Summary string   `json:"summary"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(OpenAPI, &doc); err != nil {
			panic("contract: invalid openapi document: " + err.Error())
		}
		for path, methods := range doc.Paths {
			for m, op := range methods {
				switch m {
				case "get", "post", "put", "delete", "patch":
				default:
					continue
				}
				tag := ""
				if len(op.Tags) > 0 {
					tag = op.Tags[0]
				}
				ops = append(ops, Operation{Method: strings.ToUpper(m), Path: path, Tag: tag, Summary: op.Summary})
			}
		}
		sort.Slice(ops, func(i, j int) bool {
			if ops[i].Path != ops[j].Path {
				return ops[i].Path < ops[j].Path
			}
			return ops[i].Method < ops[j].Method
		})
	})
	return ops
}
