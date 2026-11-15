# Insurance data requirements

Status: M11 draft. Entity definitions are in [domain-model.md](../architecture/domain-model.md); API shapes are in [v1-contract.md](../api/v1-contract.md).

The catalog is entered by hand by internal admins from insurer material (D1, D4). It has no live feed, so the data is only as good as its sources and dates. That makes source tracking and revision history core requirements, not extras.

## 1. Categories

| Code | Thai | English | Notes |
|---|---|---|---|
| `life` | ประกันชีวิต | Life Insurance | Term, whole life and similar. Which sub-types the MVP covers is OQ-09. |
| `health` | ประกันสุขภาพ | Health Insurance | Inpatient and outpatient medical cover. |
| `personal_accident` | ประกันอุบัติเหตุ | Personal Accident | |
| `critical_illness` | ประกันโรคร้ายแรง | Critical Illness | May be a standalone policy or a rider (OQ-10). |

- INS-01 (MUST): Categories are reference data with a stable `code` and names in both languages. The MVP has exactly these four. Admins MAY edit names and display order, but MUST NOT add categories without a scope decision.
- INS-02 (MUST): Every product belongs to exactly one category.

## 2. Providers

- INS-03 (MUST): A provider record holds: legal name (TH, EN), display name (TH, EN), insurer type (life or non-life), Office of Insurance Commission (OIC) licence reference if recorded `[VERIFY: official]`, website, public contact channel, logo (OQ-11 covers logo and trademark use), and status (`active`, `inactive`).
- INS-04 (MUST): Providers are insurers. Brokers and agents are not modelled as providers in the MVP.

## 3. Products

- INS-05 (MUST): A product record holds:
  - Category and provider.
  - Name and short description (TH, EN). The marketing name is kept exactly as the insurer publishes it.
  - Insurer product code, if published.
  - Policy term and premium-payment term, where relevant (needed for life-insurance tax eligibility, see [tax-calculation.md](tax-calculation.md)).
  - Entry-age range, maximum renewal age, waiting periods.
  - Available payment frequencies (annual, semi-annual, quarterly, monthly).
  - Status (section 8), effective date (from, and optional to), last-updated date.
  - At least one source (section 7).
- INS-06 (MUST): A URL-safe `slug`, unique and stable once published, identifies a product publicly. Internal IDs are not used in public URLs (OQ-12).

## 4. Coverage

- INS-07 (MUST): Coverage types are reference data (for example "inpatient room and board per day", "accidental death", "cancer lump sum") with a `code`, names and explanations in both languages, and a unit (`amount_thb`, `amount_thb_per_day`, `count`, `percent`, `text`).
- INS-08 (MUST): A product links to coverage types through product coverage rows holding the limit (amount, unit, per-event or per-year basis), any sub-limit, any co-payment or deductible, and notes in both languages.
- INS-09 (SHOULD): Coverage types are shared between products in a category so that comparison tables line up row by row. A product-specific item that matches no shared type is shown as a note.

## 5. Exclusions

- INS-10 (MUST): Exclusions are stored per product, with text in both languages and an optional standard exclusion code for grouping (for example pre-existing conditions, self-inflicted injury, war). The full exclusion wording stays the insurer's; SafeInSiam summaries MUST be labelled as summaries and link to the source document.

## 6. Premium rates and conditions

Indicative premiums (D5) come from rate tables entered by admins.

- INS-11 (MUST): A premium rate row holds: product, rating factors (section 6.1), sum-insured band or plan tier, payment frequency, premium amount in THB, and the effective date range.
- INS-12 (MUST): Amounts are stored as exact integers in satang (1 THB = 100 satang), never as floating point.
- INS-13 (MUST): Premium conditions record the rules that limit or adjust a rate: entry-age limits, sum-insured minimum and maximum, occupation-class restrictions, mandatory riders, loadings, and "subject to underwriting" flags. Conditions are shown to users in both languages.
- INS-14 (MUST): Rate tables are validated on save: no overlapping bands for the same factor combination, and no gaps inside the product's declared ranges. A lookup that matches no row produces "no indicative premium available", never an interpolated or guessed figure.
- INS-15 (MUST): Every displayed premium is labelled indicative and shows the rate table's source and effective date.

### 6.1 Rating factors

Which factors each category uses is OQ-13. Candidates:

| Factor | Life | Health | PA | CI | Note |
|---|---|---|---|---|---|
| Age (at last or next birthday, per product) | yes | yes | maybe | yes | Age basis per product |
| Sex | maybe | maybe | no | maybe | Using sex in pricing needs review `[VERIFY: legal]` |
| Sum insured or plan tier | yes | yes | yes | yes | |
| Payment frequency | yes | yes | yes | yes | |
| Occupation class | no | no | yes | no | |
| Smoker status | maybe | no | no | maybe | |

Health status and medical history are never rating factors in the MVP. They are sensitive personal data under the PDPA (SEC-03), and an indicative estimate does not need them.

## 7. Product sources

- INS-16 (MUST): Each product has one or more sources: type (insurer brochure, policy wording, rate table, insurer website, OIC-approved document `[VERIFY: official]`), title, URL or document reference, publisher, document date, and the date SafeInSiam retrieved it.
- INS-17 (MUST): Every coverage, exclusion and premium-rate set can be traced to a source.
- INS-18 (SHOULD): Admins can store a copy of the source document so a broken link does not lose the evidence. Where copies are stored is OQ-14, since no file storage exists today.
- INS-32 (MUST): **Published source information is frozen per revision.** A source record's master metadata (type, title, URL or document reference, publisher, document date, retrieval date, stored-copy reference) can be shared by several products. Each published product revision holds its own copy of the metadata of every source it cites, as it was at publish time (INS-21). Public views and the public API show that copy, never the current source record (INS-29). This holds however OQ-17 is resolved. A source record that any revision or working data references is never deleted. The versioning rules for the two operations below are in [v1-contract.md section 3.1](../api/v1-contract.md#31-versions-and-optimistic-concurrency):
  - *Linking or unlinking* a source to a product changes that product's working data only, and becomes public only when the product publishes a new revision.
  - *Editing a source record's master metadata* changes no published revision. It counts as an unpublished change on every product whose working data links the source, and reaches each product's public data only when that product publishes a new revision.

## 8. Status, dates and revisions

- INS-19 (MUST): Product statuses: `draft` (never public), `published` (public inside its effective dates), `suspended` (hidden temporarily, for example while data is in question), `retired` (permanently withdrawn). Allowed transitions: `draft → published`, `published ↔ suspended`, `published/suspended → retired`, and `published → published` to publish working-data changes of an already published product (J5 step 5).
- INS-20 (MUST): A product has an effective-from date, an optional effective-to date and a last-updated date. The last-updated date changes only when published content changes, and is shown publicly.
- INS-21 (MUST): Each publish action, including a status transition, creates an immutable revision: a full snapshot of the product and its coverage, exclusions, rates, conditions, tax eligibility and sources (a copy of each cited source's metadata, INS-32), plus the admin, a timestamp and a change note.
- INS-29 (MUST): **Public data comes only from published revisions.** Every public view and public API response shows the content of the product's latest published revision and nothing else. Edits an admin makes after publishing are unpublished working data. They MUST NOT change anything the public sees, including list results, comparisons, indicative premiums, tax eligibility and the last-updated date, until they are published as a new revision through the publish action (validation, change note, revision). This requirement holds however OQ-17 is resolved. OQ-17 only chooses the mechanism (public reads from the latest published snapshot, or separate draft copies).
- INS-30 (MUST): In the MVP, "approved" means published through the publish action by an admin with permission to publish (roles: OQ-07). A separate second approver is not in the MVP (see the roadmap). Changes that take effect without an edit are limited to safe ones: the effective-to date passing (INS-23) and tax eligibility becoming "under review" when its rule version is superseded (TAX-18).
- INS-31 (MUST): **Working version and published revision are separate.** The working data has a `working_version` that every successful working-data edit increments. Published state is identified by the `revision_number` of the latest published revision (`published_revision`), which only publish actions and other status transitions create. Every working-data edit and every transition states the version(s) the admin last read; the server rejects a stale value with 409 `conflict` and changes nothing, so one admin's edit can never silently overwrite another's. Each published revision records the `working_version` it was taken from. API rules: [v1-contract.md section 3.1](../api/v1-contract.md#31-versions-and-optimistic-concurrency). This holds however OQ-17 is resolved.
- INS-22 (SHOULD): The system flags products whose last-updated date is older than a set review interval (interval: OQ-14).
- INS-23 (MUST): A product past its effective-to date drops out of public results automatically.

## 9. Tax eligibility

- INS-24 (MUST): Each product records its tax-eligibility claim per tax year: the deduction category it may fall under (see [tax-calculation.md](tax-calculation.md)), the conditions it relies on (for example a policy term of 10 years or more), the evidence (for example the insurer's statement that the product qualifies), a review status (`unverified`, `verified`, `not_eligible`), and the rule version it was verified against (TAX-17).
- INS-25 (MUST): The calculator uses eligibility only if it is `verified` against the currently published rule version for that tax year. `unverified` is shown as "tax eligibility not confirmed"; eligibility verified against a superseded version is shown as "tax eligibility under review" (TAX-18). In both cases the premium does not count.
- INS-26 (MUST): Eligibility is never inferred from the category alone. For example, some life policies qualify and some do not, and premiums for personal accident cover are not assumed to be deductible.

## 10. Localisation of data

- INS-27 (MUST): Every user-facing text field has a Thai and an English value. A product cannot be published while any required field is missing in either language.
- INS-28 (MUST): Where the two languages differ in meaning, the Thai source wording wins. Each product shows a note that the English text is a translation, if it is one.
