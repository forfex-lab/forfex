# forfex

Experiment-design orchestration for programmable nucleases.

forfex plans and executes the computational half of a gene-editing
experiment: choosing an editing system, designing guides or RVD
arrays, running off-target analysis, designing validation assays, and
analysing the sequencing that comes back. It drives local CLI coding
agents as reasoning workers and runs bioinformatics tools as isolated
containerised services behind an MCP bus.

**Status: early development.** Nothing here is validated for
scientific use yet.

---

## Why

Existing agentic tooling for experiment design is CRISPR-shaped.
forfex models the editor as one field among several, so that Cas9,
Cas12a, base editors, prime editors, TALENs and ZFNs are alternatives
within one workflow rather than separate tools — and so that a design
question can be asked about a target rather than about a technology.

Two design commitments follow from the domain rather than from
software taste:

**Alleles are not cells.** A percentage of edited alleles is not a
percentage of functionally disrupted cells. forfex carries the
denominator explicitly through every data structure and every report,
because collapsing the two is the most common way editing results are
overstated.

**Provenance is a first-class field, not metadata.** Every value
knows whether it came from a published source, a local computation, or
a measurement — and that determines where it may be sent.

---

## Architecture

```
          ┌──────────────────────────────┐
          │   Go orchestrator            │
          │   state machines, task DAG   │
          │   SQLite state + audit log   │
          └───────┬──────────────┬───────┘
                  │              │
        worker adapters      MCP tool bus
                  │              │
      ┌───────────┴──┐    ┌──────┴────────────────┐
      │ claude -p    │    │ Podman containers     │
      │ codex exec   │    │ Cas-OFFinder (BSD-3)  │
      │ grok         │    │ Primer3 (GPL-2.0)     │
      │ opencode     │    │ CRISPResso2 (EULA)    │
      └──────────────┘    │ CRISPRme (AGPL-3.0)   │
                          └───────────────────────┘
```

- **Orchestrator** — Go. Tasks are state machines; a run is a DAG of
  them. All state in SQLite, every LLM call and tool call in an
  append-only audit log.
- **Workers** — local CLI coding agents, invoked headless with
  structured JSON in and out. Roles (planner, executor, critic) bind
  to workers by configuration, so model assignment is a tested
  variable rather than a hard-coded choice.
- **Tools** — each bioinformatics tool runs as its own process in its
  own rootless container, reached over MCP. Python never enters the
  orchestrator.

### Licence isolation

The process separation above is a licence requirement, not only an
isolation preference. forfex is Apache-2.0; several tools it
orchestrates are GPL, AGPL or academic-EULA. They run as separate
processes communicating at arm's length, are never linked or cgo-bound
into the binary, and are never redistributed with forfex. Build their
images locally from upstream. See `docs/third-party.md`.

---

## Clean-room notice

forfex is an independent clean-room implementation of the
architecture described in:

> Qu, Huang, Yin, Zhan, Liu, Yin, Cousins, Johnson, Wang, Shah,
> Altman, Zhou, Wang, Cong. *CRISPR-GPT for agentic automation of
> gene-editing experiments.* Nature Biomedical Engineering
> 2026;10(2):245–258. [doi:10.1038/s41551-025-01463-z](https://doi.org/10.1038/s41551-025-01463-z)

It is **not affiliated with, endorsed by, or derived from** the
authors' implementation. The reference repository `cong-lab/crispr-gpt-pub`
carries no licence and is therefore all-rights-reserved; no code,
prompts, or data from it were used. forfex was built from the paper's
public description only. See `docs/clean-room.md`.

---

## Scientific-tool policy

> Rewrite the plumbing. Fork the science. Never re-derive a scoring
> function.

The value of an established bioinformatics tool is its validation
record, not its source code. Rewriting one does not produce a better
tool; it produces a new, unvalidated instrument and makes every number
it emits contestable.

| Category | Policy |
|---|---|
| Established tools | Fork, pin, patch in-tree, upstream fixes. Never reimplement. |
| Fitted scoring models | Port published coefficients verbatim with citation. CFD, CRISTA, Doench Rule Set 2, TALEN RVD efficacy are fitted models, not algorithms. |
| Abandonware with specified algorithms | Clean rewrite acceptable. |
| Plumbing | Write freely. |

Anything forked or rewritten must pass a **differential-concordance
gate** — reproducing upstream output on a reference corpus within a
declared tolerance — before it can enter a pipeline. See
`docs/validation.md`.

---

## Safety posture

forfex is a design and analysis tool. It produces computational
output only.

Generated protocols are drafts. They carry `status: ungated-draft` in
frontmatter and a visible header, enforced in the generator rather
than by convention. forfex does not order reagents, does not interface
with suppliers, and is not a substitute for institutional biosafety
review. Work involving viral vectors, human cells or pathogen-derived
sequence requires an institutional setting, qualified supervision, and
applicable biosafety committee approval.

A sequence-provenance filter runs at the tool boundary: payloads
marked as unpublished, or containing long runs of nucleotide
sequence, are routed to local inference only and never reach an
external model provider.

---

## Requirements

- Go 1.22+
- Podman (rootless), Fedora or similar
- At least one CLI coding agent available locally
- ~50 GB for reference genomes and tool images

## Install

```bash
git clone https://github.com/forfex-lab/forfex
cd forfex
go build ./cmd/forfex
```

## Licence

Apache-2.0. See `LICENSE` and `NOTICE`.

Third-party tools orchestrated by forfex carry their own licences and
are not redistributed with it. CRISPResso2 in particular is licensed
for non-commercial academic use only and its image must never be
published to a registry.
