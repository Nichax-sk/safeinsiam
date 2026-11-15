# Product vision, scope, personas, journeys, requirements and roadmap

Status: M11 draft. See the [documentation index](../README.md) for decisions D1–D6 and the marker conventions.

## 1. Product vision

SafeInSiam is a Thailand-focused platform for comparing insurance and planning personal income tax.

> **Compare smarter. Protect better. Plan your taxes.**

People in Thailand who buy insurance often also want to know how a premium affects their personal income tax. SafeInSiam puts both questions in one place:

1. **Compare:** see life, health, personal accident and critical illness plans side by side, with coverage, exclusions and an indicative premium, in Thai or English.
2. **Plan:** estimate tax payable for Tax Year 2026 before and after adding premiums that may be deductible.

Principles:

- **Honest numbers.** Every premium is an indicative estimate (D5) and every tax figure is an estimate. Neither is a quotation, an offer or tax advice.
- **Traceable data.** Every product shows where its data came from and when it was last updated. Every tax calculation names the tax-rule version it used.
- **Bilingual.** Thai and English carry the same content (D3).
- **Neutral.** SafeInSiam does not present itself as an insurer or a licensed broker (see [compliance-security.md](compliance-security.md)).

Visual direction: **Luxury Black & Gold** (see [ui-ux.md](ui-ux.md)).

Existing Thai comparison sites, such as CheckDi, are functional inspiration only. SafeInSiam MUST NOT copy their branding, identity, layout, copy or design.

## 2. MVP scope

### 2.1 In scope

| Area | Included |
|---|---|
| Insurance categories | Life Insurance, Health Insurance, Personal Accident, Critical Illness |
| Comparison | Choose a category, enter the information that category needs, compare plans (coverage, exclusions, indicative premium), open a product detail page, find a contact channel |
| Catalog administration | Internal admins manage providers, products, categories, premium rates and conditions, coverage, exclusions, tax eligibility, sources, dates, status and revision history (D1, D4) |
| Tax calculator | Tax Year 2026 only (D6): income, existing deductions and relevant premiums in; estimated taxable income and tax payable out, before and after eligible premiums |
| Language | Thai and English across the UI, validation messages, product information, tax explanations and disclaimers (D3) |
| Disclaimers | Premium estimates, tax estimates, product data freshness, and SafeInSiam's role |

Comparison and the tax calculator have equal priority (D2): neither ships as a stub of the other.

### 2.2 Out of scope for the MVP

- Provider self-service: insurers or agents managing their own products (D4).
- Live or confirmed quotations, binding offers, policy purchase, payment or policy issuance (D5).
- Integrations with insurer, broker or government systems, including automatic catalog feeds (D1) and Revenue Department e-filing.
- End-user accounts, saved profiles or saved comparisons (see OQ-06).
- Insurance categories beyond the four listed (motor, travel, home, savings-only and others).
- Tax years other than 2026, corporate tax, and non-resident tax situations (see OQ-15).
- Personalised financial, insurance or tax advice, and recommendations presented as advice.
- Paid services, advertising placements or ranking influenced by commercial arrangements (see OQ-04).
- Native mobile apps. The web app is responsive.

## 3. User personas

These are working personas for scoping, not research findings. Validate them before investing in features they alone justify.

### P1 — Salaried employee planning tax (Thai-speaking)

- Thai salaried employee with regular monthly income who files a personal income tax return each year.
- Wants to know whether buying, or increasing, life or health insurance lowers the tax due, and by roughly how much.
- Compares two or three plans and cares most about premium, coverage limits and whether the premium is deductible.
- Uses a phone, prefers Thai, and is suspicious of hard selling.

### P2 — Family protector

- Buys or reviews cover for themselves, a spouse or parents (health and critical illness cover in particular).
- Needs clear exclusions, waiting periods and entry-age limits more than tax figures.
- Wants a way to contact someone before deciding.

### P3 — English-speaking resident

- A foreign national, or a Thai national who prefers English, living and working in Thailand.
- Needs the same product and tax information in English, including Thai-specific terms explained in plain English.
- May have tax situations the MVP does not cover (see OQ-15). The calculator must say what it does not handle.

### P4 — SafeInSiam catalog admin (internal)

- SafeInSiam staff member who enters and maintains product data from insurer documents.
- Needs fast data entry in both languages, a draft state before publishing, a record of where each figure came from, and a history of changes.
- Is accountable for data accuracy, so needs to see what changed, when and by whom.

## 4. User journeys

### J1 — Compare insurance (P1, P2, P3)

1. The user opens the site in Thai or English and can switch language at any point without losing input.
2. The user chooses a category: Life, Health, Personal Accident or Critical Illness.
3. The user enters the category's inputs (for example age, sex if the rating uses it, desired sum insured, payment frequency, occupation class for PA). Only inputs that affect the indicative premium or eligibility are asked for (SEC-03).
4. The system shows matching published plans with indicative premiums. Plans the user is ineligible for (for example outside the entry-age range) are hidden or marked ineligible with the reason.
5. The user sorts or filters, and selects two or more plans to compare side by side: coverage, limits, exclusions, waiting periods, indicative premium, tax-eligibility note, source and last-updated date.
6. The user opens a product detail page.
7. The user finds a contact channel (see J4).

Every results, comparison and detail view shows the indicative-premium disclaimer (BR-07).

### J2 — Estimate tax impact (P1, P3)

1. The user opens the tax calculator, which states it covers Tax Year 2026 (B.E. 2569) and what it does not cover.
2. The user enters income, existing deductions and the insurance premiums they pay or plan to pay.
3. The system shows estimated taxable income and tax payable before and after the eligible premiums, the difference, and which premiums counted and which did not, with the reasons (for example "over the cap" or "this category is not deductible").
4. The result names the tax-rule version and shows the estimate disclaimer.

### J3 — From comparison to tax impact (P1)

1. From a plan in J1, the user chooses "See tax impact".
2. The calculator opens with that plan's indicative annual premium and its tax-eligibility category filled in. The user adds income and deductions.
3. The result is labelled as an estimate built on an indicative premium.

Moving between J1 and J3 must not send the user's inputs to the server more often than the calculations need, and must not store them unless OQ-06 decides otherwise.

### J4 — Contact (P1, P2, P3)

1. From a product page, the user sees how to get in touch.
2. The MVP form of contact is an open question (OQ-03): a displayed provider or SafeInSiam contact channel only, or a contact form that creates a `QuoteRequest`. A contact form collects personal data and may count as regulated insurance activity, so it MUST NOT ship before the legal review in SEC-01 and SEC-02.

### J5 — Maintain the catalog (P4)

1. The admin signs in (method: OQ-07).
2. The admin creates or edits a provider, then a product in `draft`: category, names and descriptions in both languages, coverage, exclusions, premium rates and conditions, tax eligibility, sources, effective date.
3. The system validates the draft: required fields in both languages, at least one source, rate tables without gaps or overlaps.
4. The admin publishes. Publishing records a revision with who, when and a change note. Public views show only `published` products inside their effective dates, and only the content of their latest published revision (INS-29).
5. When a source changes, the admin edits the working data and then publishes it as a new revision, or suspends or retires the product. Until the edit is published, the public keeps seeing the previous revision.
6. The admin can view revision history and compare two revisions.

## 5. Business requirements

| ID | Requirement | Priority |
|---|---|---|
| BR-01 | Users can browse and compare plans in the four MVP categories. | MUST |
| BR-02 | Users can compare at least two and up to a set maximum of plans side by side (maximum: OQ-08). | MUST |
| BR-03 | Each plan shows coverage, limits, exclusions, conditions, an indicative premium, its tax-eligibility note, its source and its last-updated date. | MUST |
| BR-04 | Users can open a product detail page and find a contact channel. | MUST |
| BR-05 | The tax calculator estimates taxable income and tax payable for Tax Year 2026 before and after eligible premiums. | MUST |
| BR-06 | The calculator never treats a premium as deductible unless the product's tax eligibility and the tax-year rules say so. | MUST |
| BR-07 | Every premium is labelled as an indicative estimate wherever it appears, including shared links and printouts. | MUST |
| BR-08 | Every tax result is labelled as an estimate, not tax advice or a guaranteed outcome, and names its rule version. | MUST |
| BR-09 | All user-facing content is available in Thai and English. | MUST |
| BR-10 | Only internal admins can change the catalog. Every change becomes public only by being published as a revision. | MUST |
| BR-11 | Only `published` products inside their effective dates appear publicly. | MUST |
| BR-12 | Product ranking and ordering are neutral and explainable (for example by premium or by name). Any commercial influence on ordering is excluded from the MVP pending OQ-04. | MUST |
| BR-13 | Public launch requires completed legal and regulatory review (SEC-01, SEC-02). | MUST |
| BR-14 | Users can move from a plan to the tax calculator with its premium filled in (J3). | SHOULD |
| BR-15 | The calculator explains why each premium counted, was capped, or did not count. | SHOULD |
| BR-16 | Admins can compare two revisions of a product. | SHOULD |

## 6. Future roadmap

These items are candidates, not approved scope. Each needs its own decision before work starts.

| Horizon | Candidate |
|---|---|
| After MVP | Tax years after 2026, with rule versions added per year |
| After MVP | More categories (for example savings/endowment, pension, travel, motor), each with its own data and regulatory review |
| After MVP | End-user accounts with saved comparisons and tax scenarios, subject to PDPA design |
| After MVP | Admin review workflow with separate maker and checker roles |
| Later | Provider self-service portal (reverses D4) |
| Later | Automated catalog updates from insurer feeds (reverses D1) |
| Later | Firm quotes or referrals through licensed partners, only after regulatory review |
| Later | Wider tax coverage (other deductions in more depth, non-resident cases), each reviewed against official sources |
