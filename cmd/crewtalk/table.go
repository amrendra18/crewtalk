package main

import (
	"github.com/amrendra18/crewtalk/internal/route"
	"github.com/amrendra18/crewtalk/internal/route/claude"
	"github.com/amrendra18/crewtalk/internal/route/codex"
)

func routeTable() map[string]route.Route {
	table := map[string]route.Route{}
	for _, r := range []route.Route{codex.Route{}, claude.Route{}} {
		table[r.Kind()] = r
	}
	return table
}
