package main

import (
	"github.com/forfex-lab/forfex/internal/privacy"
	"github.com/forfex-lab/forfex/internal/task"
)

// catalog is the orchestrator's task catalogue.
//
// These three wrap `iwec`, the MCP server for the IWE knowledge graph —
// the one MCP server this project actually runs today. They are first
// because the vault is where the orchestrator's own reference material
// lives, not because reading notes is the interesting half of the
// system; the bioinformatics tasks arrive with their containers
// (Rule 6) and will be declared the same way.
//
// # How to read a Provenance declaration
//
// The question a declaration answers is narrow: if this argument went
// to an external model, would that breach Rule 4? It is not "is this
// secret". A document KEY is declared `Unknown` because a key is
// whatever the caller passed — `evidence/R03-knipping-2022-hspc` names
// a published paper and `notes/…` may name a person, and nothing in the
// string says which. `Unknown` is the honest answer to "where did this
// come from", and privacy treats it as unpublished, so the honest
// answer is also the safe one.
//
// A LIMIT is declared `Published` because an integer chosen by the
// orchestrator carries nothing from the vault. That is not a technicality:
// it is what makes `vault.find -args '{"limit":10}'` externally routable
// while the same task with a search string is not, which is the whole
// mechanism visible in one pair of commands.
//
// Nothing here declares `Published` on a field that carries caller text.
func catalog() *task.Registry {
	r := task.NewRegistry()
	r.MustRegister(
		task.Task{
			Name:    "vault.stats",
			Summary: "Graph statistics for the knowledge base, or for one document",
			Worker:  "vault",
			Tool:    "iwe_stats",
			// The call with no arguments is the tool name and nothing
			// else, so the baseline is genuinely publishable. Supplying
			// `key` raises it, which is the point of a floor.
			Baseline: privacy.Published,
			Inputs: []task.Input{
				{
					Name:       "key",
					Provenance: privacy.Unknown,
					Summary:    "Scope the statistics to one document key",
				},
			},
		},
		task.Task{
			Name:     "vault.find",
			Summary:  "Search the knowledge graph by text or title",
			Worker:   "vault",
			Tool:     "iwe_find",
			Baseline: privacy.Published,
			Inputs: []task.Input{
				{Name: "lexical", Provenance: privacy.Unknown,
					Summary: "BM25 full-text match on title and body"},
				{Name: "fuzzy", Provenance: privacy.Unknown,
					Summary: "Fuzzy match on title and key"},
				{Name: "project", Provenance: privacy.Unknown,
					Summary: "Projection: which fields each result carries"},
				{Name: "limit", Provenance: privacy.Published,
					Summary: "Maximum number of results"},
			},
		},
		task.Task{
			Name:     "vault.retrieve",
			Summary:  "Fetch documents by key, with their links and context",
			Worker:   "vault",
			Tool:     "iwe_retrieve",
			Baseline: privacy.Published,
			Inputs: []task.Input{
				{Name: "keys", Provenance: privacy.Unknown,
					Summary: "Document keys to retrieve"},
				{Name: "search", Provenance: privacy.Unknown,
					Summary: "Retrieve by search instead of by key"},
				{Name: "limit", Provenance: privacy.Published,
					Summary: "Maximum number of documents"},
				{Name: "max_tokens", Provenance: privacy.Published,
					Summary: "Cap total projected content tokens"},
			},
		},
	)
	return r
}

// Deliberately absent: iwe_create, iwe_update, iwe_delete, iwe_rename,
// iwe_extract, iwe_inline, iwe_squash, iwe_attach and iwe_normalize.
//
// Every one of them writes to the graph, and iwe_normalize rewrites
// EVERY document in the library — the trap CLAUDE.md documents, where
// the vault root is the library root and a bare {{date}} in frontmatter
// silently gutted a template. A catalogue is a list of things something
// upstream may invoke without a human in the loop, and nothing in this
// orchestrator yet decides when a write is warranted. Read-only is not
// a limitation of the mechanism; it is the current extent of what has
// been thought through.
