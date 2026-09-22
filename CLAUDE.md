# forfex — standing rules

Programmable-nuclease experiment design orchestrator.
Independent research project. Tracking: Linear team `cgtru`,
project "CRISPR-GPT Reimplementation (Lab)".

Clean-room reimplementation of the architecture described in
Qu, Huang, Cong et al., Nat Biomed Eng 2026;10(2):245-258,
doi:10.1038/s41551-025-01463-z — generalized beyond CRISPR to
TALENs and ZFNs. Not affiliated with or derived from that project.

## Repos
- `forfex-lab/forfex` — Go orchestrator, MCP shims, containers
- `forfex-lab/forfex-vault` — knowledge base, Markdown only

## Rule 1 — Clean room (CGTRU-12)
`cong-lab/crispr-gpt-pub` has NO LICENSE -> all-rights-reserved.
READ-ONLY architectural reference. Never copy its code, prompts,
or structure. Rebuild from the paper's public description only.
If you find yourself transcribing, stop.

## Rule 2 — Rewrite the plumbing, fork the science (CGTRU-19)
Never re-derive a fitted scoring model. CFD, CRISTA, Doench
Rule Set 2, TALEN RVD efficacy have PUBLISHED COEFFICIENTS —
port them verbatim with citation.
- Fork + maintain: CRISPResso2, CRISPRme, Cas-OFFinder, Primer3
- Clean rewrite OK: TALE-NT talesf, Mojo Hand (abandonware,
  fully specified algorithms)
- Write freely: MCP shims, orchestrator, schedulers, reports
Anything forked or rewritten needs a differential-concordance
result against upstream before it enters the pipeline.

## Rule 3 — Bench gate (CGTRU-22)
NO physical work. Ordering oligos or constructs IS bench work.
Every generated protocol carries `status: ungated-draft` in
frontmatter plus a visible header. Enforced in the generator,
never by convention.

## Rule 4 — Sequence privacy (CGTRU-18)
Unpublished lab sequence and anything from eLabFTW routes to
LOCAL INFERENCE ONLY. Never to Claude, Codex, Grok, opencode.
Enforced at the tool boundary in Go, not by prompt.
Also block any payload with >=20 consecutive A/T/G/C/U from
reaching an external model.

Tool calls go through `internal/task`. A task declares every
argument it accepts and the provenance of each; the payload's
provenance is DERIVED from the ones actually supplied, never
asserted by the caller. An undeclared argument is refused
before dispatch, because it would carry no declaration.
`forfex call` still takes an asserted provenance — it is the
debugging path, not the production one.

## Rule 5 — Auth (CGTRU-11)
`claude -p` on this machine only. No Agent SDK (requires an API
key). No third-party harness reusing subscription OAuth.

## Rule 6 — Licence isolation
Never link, vendor, or cgo-bind an AGPL/GPL tool into the forfex
binary. Every external bioinformatics tool runs as a separate
process in its own container, reached over MCP. This is a licence
requirement, not just an isolation preference.
CRISPResso2's academic EULA means its image is NEVER redistributed
— build locally from upstream, never push to a public registry.

## Style
Go for the orchestrator. Python only inside Podman containers,
never imported by the core. Cite primary sources for every
scientific claim. Say "I don't know" rather than guessing.

