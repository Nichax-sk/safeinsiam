# Open questions

Status: M11. Each question needs a decision before the work that depends on it starts. Nothing here is decided. When a question is answered, record the decision, date and decider here, and update the documents that reference it.

| ID | Question | Blocks | Owner |
|---|---|---|---|
| OQ-01 | Where will SafeInSiam be hosted, and in which country is data stored? (Affects SEC-06c and TLS/proxy setup.) | Deployment, PDPA assessment | Product / engineering |
| OQ-02 | Who carries out the legal and regulatory review (SEC-01, SEC-02), and by when is it needed before launch? | Public launch | Product |
| OQ-03 | What is the MVP contact channel: show provider or SafeInSiam contact details only, or a contact form that stores a `QuoteRequest`? If a form, who receives the requests, and does passing them on need a licence? | J4, `POST /quote-requests`, QuoteRequest | Product + legal |
| OQ-04 | Will SafeInSiam receive payment from insurers (fees, commissions, sponsored placement)? If so, how is it disclosed and how does it affect ordering? | BR-12, SEC-02 | Product + legal |
| OQ-05 | Are tax calculations ever stored (`TaxCalculation`)? Proposal: no, in the MVP. | TaxCalculation, SEC-06 | Product |
| OQ-06 | Are comparisons stored (`ComparisonSession`), for example for shareable links? Proposal: no; shareable state only in the URL without personal data. | ComparisonSession, UX-15 | Product |
| OQ-07 | How do admins authenticate (local accounts, single sign-on), and what roles exist (for example editor, publisher, tax-rule reviewer)? | All admin routes, AdminUser | Engineering + security |
| OQ-08 | What is the maximum number of plans compared side by side (proposal: 4)? | BR-02, `POST /comparisons` | Product / UX |
| OQ-09 | Which life-insurance sub-types are in the MVP (term, whole life, endowment/savings, unit-linked, annuity/pension)? Annuity/pension has different tax treatment. | Catalog, tax eligibility | Product |
| OQ-10 | Are critical illness products standalone policies, riders, or both, and how is their tax eligibility determined? | CI category, TAX-08 | Product + official verification |
| OQ-11 | May SafeInSiam show insurer names, logos and product names, and on what terms? | Provider and product display | Legal |
| OQ-12 | ID strategy for tables (`uuid` vs `bigint identity`) and the format of public opaque IDs. | Schema (later milestone) | Engineering |
| OQ-13 | Which rating factors does each category use for indicative premiums, and is using sex in pricing acceptable? | PremiumRate, category inputs | Product + legal |
| OQ-14 | Where are copies of source documents stored (no file storage exists today), and after how long is a product flagged for re-review? | INS-18, INS-22 | Engineering + product |
| OQ-15 | Which taxpayer situations and income types are in the MVP calculator beyond employment income (freelance, rental, investment income, non-residents)? | TAX-01, calculator inputs | Product |
| OQ-16 | Bilingual storage: paired `*_th`/`*_en` columns (proposal) or a translation table? | Schema | Engineering |
| OQ-17 | Which mechanism keeps edits to a published product from the public until they are re-published: public reads from the latest published snapshot (proposal), or separate draft copies? The requirement itself is fixed (INS-29); only the mechanism is open. | Revision model | Engineering |
| OQ-18 | Final `Cache-Control` values per route. | API | Engineering |
| OQ-19 | How is rate limiting done without new infrastructure (in-process limiter vs at the proxy)? | SEC-10 | Engineering |
| OQ-20 | How are tax rule sets authored and reviewed in the MVP: reviewed data files or migrations (proposal), or an admin UI? | TaxRule | Engineering + product |
| OQ-21 | Rounding rules for percentage caps and per-bracket tax, pending official verification. | TAX-11 | Official verification |
| OQ-22 | Brand assets: logo, wordmark, exact palette, Thai and Latin typefaces, and whether fonts are self-hosted. | UX-01–UX-03 | Product / design |
| OQ-23 | Default language for first visits, and whether the URL carries the language prefix. | UX-06, UX-07 | Product |
| OQ-24 | How is i18n implemented in Nuxt: a hand-written composable with message files, or an i18n module (a new dependency, which needs approval)? | UX-04–UX-07 | Engineering |
| OQ-25 | Do product descriptions need rich text (lists, links), and if so in what safe format? | SEC-12, admin editor | Product + engineering |
| OQ-26 | For deductions that depend on a spouse's or parent's circumstances (TAX-16): are conditions asked as yes/no answers or as amounts? How are two parents (and possibly in-laws) represented? Is sharing a parent's deduction between siblings in scope? Should the result also show what the tax would be if the condition were met? Asking for amounts collects third-party personal data (SEC-03). | TAX-16, calculator inputs, `POST /tax/calculations` | Product + official verification + legal |
| OQ-27 | When a new rule version is published within a tax year (TAX-18): may a reviewer re-verify unchanged deduction categories in bulk, or must each product be re-verified individually? May the new version be published before affected products are re-verified (they then show "under review"), or is publishing blocked until they are? | TAX-18, admin workflow | Product + engineering |

## Assumptions made in M11

These are working assumptions, not decisions. Confirm or correct them.

1. The calculator's primary user is a Thai tax resident with employment income (TAX-01).
2. Tax Year 2026 means calendar year 2026 (B.E. 2569) income, filed in 2027.
3. Comparison and tax calculation are stateless in the MVP (OQ-05, OQ-06).
4. Public API responses carry both languages and the frontend selects one.
5. Money is stored and transferred as integer satang.
6. Health information is never collected in the MVP.
