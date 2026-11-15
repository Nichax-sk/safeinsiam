# API contract: `/api/v1` (proposed)

Status: **M11 proposal. Not implemented.** Today the backend serves only `GET /health` and `GET /health/db`, and `/api/v1` has no routes. This contract describes the MVP endpoints to build in later milestones. It reuses the existing conventions in [`CLAUDE.md`](../../CLAUDE.md) section 3 and does not change them.

## 1. Conventions

### 1.1 Inherited from the existing backend (unchanged)

- Routes register on `router.V1` under `/api/v1`.
- JSON request bodies are read only with `httpapi.DecodeJSON`: `Content-Type: application/json` (optional `charset=utf-8`), 1 MiB limit, one JSON value, unknown fields rejected.
- Every error response uses the existing envelope and codes:

  ```json
  {"error":{"code":"validation_failed","message":"Request validation failed","request_id":"3QX7…","details":[{"field":"income.salary","code":"invalid_type"}]}}
  ```

- `X-Request-ID` is accepted, echoed and included in errors. It is for correlation only.
- CORS policy stays fixed: methods `GET, POST, PUT, PATCH, DELETE`; request headers `Content-Type, X-Request-ID`; no credentials. Frontend code uses uppercase `PATCH`.
- Database errors go through `httpapi.DatabaseError`. No driver text reaches a response.
- Request bodies, query strings and personal data are never logged (see SEC-05).

### 1.2 New conventions proposed for feature routes

- **Localisation.** Responses carry both languages for each text field as `{"th": "...", "en": "..."}`. The frontend picks the language. This keeps one cacheable response per URL and needs no `Vary: Accept-Language`. API error `message` values stay the fixed English defaults; the frontend shows a localised message chosen from `code` and each detail `code` (UX-05).
- **Body decoding errors** come from `httpapi.DecodeJSON` (`backend/internal/httpapi/decode.go`) and feature handlers pass them to `WriteError` unchanged, with no remapping:
  - 415 `unsupported_media_type` and 413 `payload_too_large`, without details.
  - 400 `bad_request` with a detail code and no `field`: `unreadable_body`, `empty_body`, `malformed_json`, `trailing_data`, `null_body`, `unknown_field`, `invalid_body`, and `invalid_type` when the **top-level** value has the wrong type.
  - 422 `validation_failed` with `invalid_type` and a `field` when a **field** has the wrong JSON type (for example `{"field":"expected_working_version","code":"invalid_type"}`).
- **Validation detail codes** that feature handlers add after a body decodes: `required`, `out_of_range`, `invalid_value`, `too_long`, `too_many`, `not_available`. They are used only with 422 `validation_failed`. Detail `field` uses the JSON field path, as `DecodeJSON` does.
- **Conflict detail codes** are used only with 409 `conflict`: `stale_version` (a version precondition did not match; section 3.1). Checks run in order: body decoding (400/413/415/422 above), then feature validation (422), then version preconditions (409). A request that fails decoding or validation is never reported as a conflict.
- **Money** is an integer number of satang in a field ending `_satang` (for example `premium_satang: 1250000` = ฿12,500.00). No floats.
- **Dates** are `YYYY-MM-DD` (Gregorian). Timestamps are RFC 3339 in UTC. Tax years are Gregorian integers (`2026`); the UI also shows the Buddhist Era year (B.E. 2569).
- **Lists** return `{"items": [...], "next_cursor": "..."|null}` with `limit` (default 20, max 100) and opaque `cursor` query parameters.
- **Identifiers** in URLs are slugs for public catalog resources and opaque IDs elsewhere.
- **Caching.** `Cache-Control` is set per route (CLAUDE.md leaves it undecided). Proposed: public catalog `GET`s `public, max-age=300`; calculation and admin responses `no-store`. Final values: OQ-18.
- **Disclaimers in data.** Responses that contain an indicative premium or a tax estimate include a `disclaimer` object (`{"code": "...", "th": "...", "en": "..."}`) so no client can show the figure without the text.

## 2. Public endpoints

### 2.1 Categories

`GET /api/v1/categories` → 200

```json
{"items":[{"code":"health","name":{"th":"ประกันสุขภาพ","en":"Health Insurance"},"description":{"th":"…","en":"…"},"input_fields":["age","sex","plan_tier","payment_frequency"]}],"next_cursor":null}
```

`input_fields` lists the inputs the category's estimate uses (rating factors: OQ-13).

### 2.2 Products

`GET /api/v1/products?category=health&provider=…&limit=20&cursor=…` → 200

Returns only `published` products inside their effective dates.

```json
{"items":[{"slug":"example-health-plus","category":"health","provider":{"slug":"example-life","name":{"th":"…","en":"…"}},"name":{"th":"…","en":"…"},"summary":{"th":"…","en":"…"},"last_updated_at":"2026-10-01","tax_eligibility":{"tax_year":2026,"status":"verified","deduction_category":"health_insurance_self","verified_against_rule_version":"2026.1"}}],"next_cursor":null}
```

- Public product endpoints return only the content of each product's latest published revision. Unpublished admin edits never appear in them (INS-29).
- `tax_eligibility.status` is `verified`, `unverified`, `not_eligible` or `under_review`. `under_review` is derived when `verified_against_rule_version` is no longer the published rule version for that tax year (TAX-18).

`GET /api/v1/products/{slug}` → 200, or 404 `not_found` for unknown, draft, suspended or retired products.

Body: the list fields plus `coverages[]` (`code`, `name`, `limit`, `basis`, `notes`), `exclusions[]` (`summary`, `is_summary`), `conditions[]` (`kind`, `description`), `policy_term_years`, `premium_term_years`, `entry_age`, `waiting_period_days`, `payment_frequencies`, `effective_from`, `effective_to`, `sources[]` (`type`, `title`, `url`, `document_date`, `retrieved_at`), `contact` (OQ-03), `published_revision` (number of the revision shown), and `disclaimer`. Public responses never include `working_version`.

`sources[]`, like every other field, comes from the published revision: it is the source metadata captured when that revision was published, not the current source record. Later edits to a source record do not appear here until the product publishes a new revision (INS-32).

### 2.3 Indicative premium comparison

`POST /api/v1/comparisons` → 200

Computes indicative premiums for the given inputs. Stateless in the MVP: nothing is stored (OQ-06).

Request:

```json
{"category":"health","inputs":{"age":35,"sex":"female","plan_tier":"standard","payment_frequency":"annual"},"product_slugs":["example-health-plus","other-health"]}
```

- `product_slugs` is optional; without it, all eligible published products in the category are returned.
- Inputs not listed in the category's `input_fields` are rejected (422 `invalid_value`).
- No name, contact, ID number or health information is accepted.

Response:

```json
{"category":"health","results":[{"product_slug":"example-health-plus","eligible":true,"indicative_premium":{"amount_satang":1250000,"payment_frequency":"annual","annualised_satang":1250000,"rate_effective_from":"2026-01-01","source":{"title":"…","document_date":"2026-01-15"}},"ineligible_reasons":[]},{"product_slug":"other-health","eligible":false,"indicative_premium":null,"ineligible_reasons":[{"code":"entry_age_exceeded","th":"…","en":"…"}]}],"disclaimer":{"code":"indicative_premium","th":"…","en":"…"}}
```

- A product with no matching rate row returns `indicative_premium: null` with reason `no_rate_available`; no figure is guessed (INS-14).
- Maximum products per comparison: OQ-08 (422 `too_many` when exceeded).

### 2.4 Tax

`GET /api/v1/tax/years` → 200: supported tax years and their published rule version.

```json
{"items":[{"tax_year":2026,"tax_year_be":2569,"rule_version":"2026.1","status":"published","verified_at":"…"}],"next_cursor":null}
```

`GET /api/v1/tax/years/{year}/rules` → 200: the published rule set (brackets, allowances, deduction categories with caps and conditions, source references) for display and explanation. 404 for unsupported years.

`POST /api/v1/tax/calculations` → 200. Stateless in the MVP (OQ-05).

Request:

```json
{"tax_year":2026,"income":{"employment_satang":72000000,"other":[]},"deductions":{"personal":true,"spouse":false,"children":0,"social_security_satang":900000,"provident_fund_satang":0,"other":[]},"insurance_premiums":[{"source":"product","product_slug":"example-health-plus","annual_premium_satang":1250000,"insured":"self"},{"source":"manual","deduction_category":"life_insurance","annual_premium_satang":3000000,"insured":"self"}]}
```

- `income` and `deductions` fields are defined by the published rule set for the year; unknown fields are rejected. The full field list is set when the 2026 rules are verified (TAX-02).
- A `manual` premium (a policy the user already holds) states its deduction category. The user's statement is shown as user-declared (`counted_user_declared`), never as verified.
- `insured` (`self`, `spouse`, `parent`) matters because caps differ by insured person `[VERIFY: official]`.
- A premium for a `spouse` or `parent`, and any allowance in `deductions` that depends on another person, carries the condition answers the rule set requires, for example `"conditions":{"spouse_has_no_income":true}`. Each answer is `true`, `false` or `null` ("don't know"); an omitted answer is treated as `null`. Field names are fixed with the verified 2026 rules (TAX-02) and the inputs chosen in OQ-26. Results per TAX-16:
  - all `true`: evaluated against the other eligibility rules and caps; if counted, status `counted_user_declared` with reason `user_declared_condition`.
  - any `false`: not counted, status `condition_not_met`, with a `condition_not_met` reason naming each condition answered `false`.
  - otherwise any `null` or omitted: not counted, status `condition_unconfirmed`, with a `condition_missing` reason naming each unanswered condition.

Response:

```json
{"tax_year":2026,"rule_version":"2026.1","before":{"assessable_income_satang":72000000,"expense_deduction_satang":10000000,"allowances_satang":6900000,"taxable_income_satang":55100000,"tax_payable_satang":3515000},"after":{"taxable_income_satang":50850000,"tax_payable_satang":2877500},"difference":{"tax_payable_satang":-637500},"premiums":[{"index":0,"counted_satang":1250000,"status":"counted","reasons":[]},{"index":1,"counted_satang":3000000,"status":"counted_user_declared","reasons":[{"code":"user_declared_category","th":"…","en":"…"}]}],"allowances":[{"code":"personal","applied_satang":6000000,"status":"counted","reasons":[]},{"code":"social_security","applied_satang":900000,"status":"counted","reasons":[]}],"bracket_breakdown":[],"limitations":[{"code":"not_covered_non_resident","th":"…","en":"…"}],"disclaimer":{"code":"tax_estimate","th":"…","en":"…"}}
```

The figures above illustrate the shape only. They were worked out with the candidate values in [tax-calculation.md](../product/tax-calculation.md) section 4, which are unverified for 2026, and MUST NOT be used as test fixtures until those values are verified.

- Premium `status`: `counted`, `counted_user_declared`, `capped`, `not_eligible`, `eligibility_unconfirmed`, `condition_not_met`, `condition_unconfirmed`. `eligibility_unconfirmed` includes eligibility verified against a superseded rule version (TAX-18). `counted_user_declared` means the item counted but relies on something only the user stated (a declared category or a `true` condition answer); it is never described as verified.
- `allowances[]` explains each allowance and deduction the user claimed, with the same status vocabulary where it applies (`counted`, `counted_user_declared`, `capped`, `condition_not_met`, `condition_unconfirmed`), so an allowance that is not applied always carries a reason.
- A year without a published rule set returns 422 with detail `tax_year` / `not_available`.

### 2.5 Contact requests (blocked on OQ-03 and legal review)

`POST /api/v1/quote-requests` → 201. **Not built until OQ-03 is decided and SEC-01/SEC-02 review is complete.**

Request: `product_slug`, `name`, `phone` or `email`, `preferred_language`, `consent` (`{"privacy_notice_version":"…","accepted":true}`). No health, income or ID-number fields. Response: `{"id":"…","status":"received"}`. Rate-limited (429 `too_many_requests`; mechanism: OQ-19).

## 3. Admin endpoints

All under `/api/v1/admin`. Authentication and authorisation are **not designed yet** (OQ-07). Until they are, no admin route may be deployed anywhere reachable. Notes for that design:

- The current CORS policy allows no credentials and only `Content-Type, X-Request-ID` request headers. Cookie sessions would need CSRF protection (CLAUDE.md: "CSRF protection belongs with authentication"); bearer tokens would need `Authorization` added to the CORS allow list. Either is a reviewed change to `cors.go`.
- Unauthenticated → 401 `unauthorized`; authenticated without permission → 403 `forbidden`.
- Admin responses are `Cache-Control: no-store`.

| Method and path | Purpose |
|---|---|
| `GET /admin/providers`, `POST /admin/providers` | List, create providers |
| `GET/PATCH /admin/providers/{id}` | Read, update a provider |
| `GET /admin/products?status=&category=` , `POST /admin/products` | List (all statuses), create a draft |
| `GET/PATCH /admin/products/{id}` | Read, update working data |
| `PUT /admin/products/{id}/coverages` | Replace the product's coverage rows |
| `PUT /admin/products/{id}/exclusions` | Replace exclusions |
| `PUT /admin/products/{id}/premium-rates` | Replace the rate table (validated as a whole, INS-14) |
| `PUT /admin/products/{id}/premium-conditions` | Replace conditions |
| `PUT /admin/products/{id}/tax-eligibility/{year}` | Set or re-verify the tax-eligibility claim for a year, against the currently published rule version (TAX-17) |
| `POST /admin/products/{id}/sources`, `DELETE /admin/products/{id}/sources/{source_id}?expected_working_version=7` | Link, unlink sources |
| `POST /admin/products/{id}/transitions` | Change status: body `{"to":"published","change_note":"…","expected_working_version":7,"expected_published_revision":3}` |
| `GET /admin/products/{id}/revisions`, `GET /admin/products/{id}/revisions/{n}` | Revision history, one snapshot |
| `GET /admin/products/{id}/revisions/{a}/diff/{b}` | Compare two revisions (BR-16) |
| `GET/POST /admin/sources`, `PATCH /admin/sources/{id}` | Manage source records (master metadata; section 3.1) |
| `GET /admin/coverages?category=`, `POST /admin/coverages` , `PATCH /admin/coverages/{id}` | Manage shared coverage types |
| `GET /admin/tax-rules`, `GET /admin/tax-rules/{id}` | View rule versions |

- **Working data vs published data.** `PATCH`, `PUT`, and source link/unlink writes change only the product's unpublished working data. Only `POST /admin/products/{id}/transitions` changes what the public sees, and every transition creates a published revision (INS-29, INS-30). How working data is stored depends on OQ-17; this API behaviour does not.
- **Publishing** validates the whole product (both languages, at least one source, rate table, effective dates) and returns 422 `validation_failed` with details on failure.
- **Tax rule authoring** happens through reviewed data changes in the MVP, not an admin UI (OQ-20). The API only reads them.

### 3.1 Versions and optimistic concurrency

A product has two separate version numbers (INS-31). They are never interchangeable.

| Field | Meaning | Changes when |
|---|---|---|
| `working_version` | Version of the product's unpublished working data (all of it: product fields, coverages, exclusions, rates, conditions, tax eligibility, linked sources and the current master metadata of those sources). Starts at 1 when the product is created. | Every successful working-data write increments it by 1, and so does every edit to the master metadata of a source the working data links (see Sources below). Transitions do not change it. |
| `published_revision` | `revision_number` of the latest immutable published revision; `null` if never published. | Every successful transition creates the next revision. Working-data writes never change it. |

Rules:

- **Reads.** `GET /admin/products/{id}` returns the working data plus `working_version`, `published_revision`, `published_from_working_version` (the `working_version` the latest published revision was taken from; `null` if never published) and `has_unpublished_changes` (derived: `working_version` differs from `published_from_working_version`). List responses include `working_version` and `published_revision` per item.
- **Working-data writes** (`PATCH /admin/products/{id}`, every `PUT /admin/products/{id}/…`, `POST`/`DELETE /admin/products/{id}/sources…`) MUST carry `expected_working_version`: a body field, or a query parameter on `DELETE`. It is a field rather than an `If-Match` header because the CORS policy allows only `Content-Type` and `X-Request-ID` request headers.
- **Check and apply atomically.** The server applies a write only if `expected_working_version` equals the current `working_version`, and increments `working_version` in the same atomic step. The server never merges concurrent edits. Error responses keep validation and concurrency apart:

  | Case | Response | Detail |
  |---|---|---|
  | Version field missing | 422 `validation_failed` | `{"field":"expected_working_version","code":"required"}` |
  | Version field of the wrong JSON type | 422 `validation_failed` | `{"field":"expected_working_version","code":"invalid_type"}` (from `DecodeJSON`) |
  | Version present and valid, but not the current value | 409 `conflict` | `{"field":"expected_working_version","code":"stale_version"}` |

  The same table applies to `expected_published_revision` and `expected_version`. On any of these errors nothing is changed.
- **Responses.** A successful write returns the new `working_version`. After a 409, the client re-reads the product and the admin reapplies their change; the client must not resend with the new number without showing the admin the current data.
- **Transitions** MUST carry both `expected_working_version` and `expected_published_revision` (`null` for a never-published product). A missing field is 422 `validation_failed` / `required`; a stale one is 409 `conflict` / `stale_version` naming the stale field. Publishing (`to: published`) snapshots exactly the working data at `expected_working_version`, including a copy of each linked source's master metadata, so the admin publishes what they reviewed. Suspending and retiring carry forward the latest published content with the new status and never publish working data. A transition creates published revision `published_revision + 1` and leaves `working_version` unchanged.
- **Granularity.** `working_version` covers the whole product, so two admins editing different parts of the same product also conflict. Safe but coarse; acceptable for the MVP's small admin team.
- **Sources** have two separate operations (INS-32):
  - *Linking or unlinking* a source (`POST`/`DELETE /admin/products/{id}/sources…`) is a working-data write on that product: it carries `expected_working_version` and increments that product's `working_version`.
  - *Editing a source record's master metadata* (`PATCH /admin/sources/{id}`) carries the source's own `expected_version` and increments the source's `version`. In the same atomic step it increments the `working_version` of every product whose working data links that source. So the edit shows up as an unpublished change on those products, and an admin who reviewed the product before the source edit gets 409 `conflict` / `stale_version` if they then try to publish. The edit never changes a published revision: public responses keep showing the source metadata captured in each product's published revision until that product publishes a new revision.
- **Other admin resources** (providers, shared coverage types) are not revisioned. Each carries its own `version`; updates carry `expected_version`, with the same atomic check and the same error table.
