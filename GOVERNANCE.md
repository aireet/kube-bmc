# Governance

kube-bmc is a maintainer-led open source project. This document describes how decisions are
made and how contributors become maintainers.

## Roles

**Contributors** are everyone who files issues, reviews pull requests, writes documentation or
code, or helps users.

**Maintainers** are listed in [MAINTAINERS.md](MAINTAINERS.md). They review and merge pull
requests, triage issues, cut releases and steer the roadmap.

## Decision making

- Day-to-day changes are made through pull requests. A pull request needs approval from at least
  one maintainer who is not its author, and passing CI, before it is merged.
- Changes to the public API (`bmc.kube-bmc.io` resources, chart values, CLI flags, MCP tools),
  security-relevant behavior or this governance require a design proposal in
  [`docs/proposals`](docs/proposals) and approval by a majority of maintainers.
- Maintainers seek consensus. If consensus cannot be reached, the maintainers decide by simple
  majority vote, recorded in the related issue or pull request.

## Becoming a maintainer

A contributor can be nominated by an existing maintainer after sustained contributions over at
least three months, such as merged pull requests, reviews and issue triage. The nomination is
accepted when a majority of the existing maintainers approve it and none of them objects within
two weeks.

Maintainers who are inactive for six months may be moved to emeritus status by a majority vote
of the other maintainers.

## Code of conduct

The project follows the [CNCF Code of Conduct](CODE_OF_CONDUCT.md). Violations can be reported
to the maintainers or to the CNCF as described there.

## Changes to this document

Changes to this document require a pull request approved by a two-thirds majority of the
maintainers.
