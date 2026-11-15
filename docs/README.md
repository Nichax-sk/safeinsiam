# SafeInSiam documentation

Status: **M11 draft, documentation only.** These documents set the MVP product scope and the proposed API contract. Nothing described here is implemented yet. The running code is described in [`CLAUDE.md`](../CLAUDE.md), and where these documents propose something that differs from the running code, the code is the current truth until a later milestone changes it.

## Approved product decisions

| ID | Decision |
|---|---|
| D1 | The insurance catalog is entered and maintained manually by admins. There are no live insurer feeds. |
| D2 | Insurance comparison and the tax calculator have equal priority. |
| D3 | The platform is bilingual: Thai and English. |
| D4 | Internal SafeInSiam admins manage insurance products. Providers do not manage their own products in the MVP. |
| D5 | Premiums are indicative only. They are never confirmed quotations. |
| D6 | The first tax year the calculator supports is Tax Year 2026. |

## Documents

| # | Topic | Document |
|---|---|---|
| 1 | Product vision | [product/vision-and-scope.md](product/vision-and-scope.md#1-product-vision) |
| 2 | MVP scope and out of scope | [product/vision-and-scope.md](product/vision-and-scope.md#2-mvp-scope) |
| 3 | User personas | [product/vision-and-scope.md](product/vision-and-scope.md#3-user-personas) |
| 4 | User journeys | [product/vision-and-scope.md](product/vision-and-scope.md#4-user-journeys) |
| 5 | Business requirements | [product/vision-and-scope.md](product/vision-and-scope.md#5-business-requirements) |
| 6 | Insurance data requirements | [product/insurance-data.md](product/insurance-data.md) |
| 7 | Domain model and entity relationships | [architecture/domain-model.md](architecture/domain-model.md) |
| 8 | API contract | [api/v1-contract.md](api/v1-contract.md) |
| 9 | Tax calculation requirements | [product/tax-calculation.md](product/tax-calculation.md) |
| 10 | UI/UX requirements | [product/ui-ux.md](product/ui-ux.md) |
| 11 | Compliance and security requirements | [product/compliance-security.md](product/compliance-security.md) |
| 12 | Future roadmap | [product/vision-and-scope.md](product/vision-and-scope.md#6-future-roadmap) |
| — | Open questions (all documents) | [product/open-questions.md](product/open-questions.md) |

## Conventions used in these documents

- Requirement IDs: `BR-` business, `INS-` insurance data, `TAX-` tax, `UX-` UI/UX, `SEC-` compliance and security. IDs are stable; retire one rather than reuse it.
- **MUST / SHOULD / MAY** have their usual meaning (MUST is required for the MVP).
- `[VERIFY: legal]` marks an item that needs review by a qualified Thai lawyer. `[VERIFY: official]` marks an item that must be checked against an official source (Thai Revenue Department, Office of Insurance Commission) before implementation. Neither marker is a legal or regulatory conclusion.
- `OQ-nn` refers to an entry in [open-questions.md](product/open-questions.md).
- The architecture stays as it is: Nuxt 4 / Vue 3 / TypeScript / Tailwind CSS 4 frontend, Go / Gin backend, PostgreSQL 17 through pgx/pgxpool, REST under `/api/v1`. These documents introduce no new frameworks, ORMs, dependencies, paid services or infrastructure. Where a requirement could need one (for example i18n tooling or authentication), it is recorded as an open question.
