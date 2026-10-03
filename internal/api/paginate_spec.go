package api

import (
	"strings"
	"sync"

	"github.com/dorkitude/cfctl/internal/apispec"
)

// The embedded OpenAPI spec tells All which query parameters page each
// endpoint, so `cfctl api request` and generated commands page correctly
// for every style the API uses (page/per_page, page_no, page/page_size,
// cursor, page_token, continuation_token, continuationToken, next_cursor,
// scan_cursor, offset/limit, before).

var (
	specOnce  sync.Once
	specTmpls map[string][]string      // method → templated paths
	specPages map[string][]*Pagination // method → pagination, parallel to specTmpls
)

func loadSpecPaging() {
	specTmpls = map[string][]string{}
	specPages = map[string][]*Pagination{}
	ops := apispec.Ops()
	for i := range ops {
		o := &ops[i]
		var names []string
		for _, p := range o.QueryParams() {
			names = append(names, p.Name)
		}
		specTmpls[o.Method] = append(specTmpls[o.Method], o.Path)
		specPages[o.Method] = append(specPages[o.Method], PaginationFromParams(names))
	}
}

func specPagination(method, path string) *Pagination {
	specOnce.Do(loadSpecPaging)
	method = strings.ToUpper(method)
	if method == "" {
		method = "GET"
	}
	i := MatchTemplate(specTmpls[method], path)
	if i < 0 {
		return nil
	}
	return specPages[method][i]
}

func init() { SpecPagination = specPagination }
