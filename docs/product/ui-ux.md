# UI/UX requirements

Status: M11 draft. Implementation uses the existing frontend stack: Nuxt 4, Vue 3, TypeScript and Tailwind CSS 4 utility classes. No UI component library, design tool or font service is added by this document.

## 1. Visual direction: Luxury Black & Gold

- UX-01 (MUST): A dark, restrained base (near-black backgrounds, charcoal surfaces) with gold as the accent for primary actions, highlights and key figures. Gold is used sparingly so it keeps meaning.
- UX-02 (MUST): Colours are defined once as design tokens (Tailwind 4 `@theme` variables in `app/assets/css/main.css`), not hard-coded per component.
- UX-03 (MUST): Text and interactive elements meet WCAG 2.2 AA contrast. Gold on black is checked per shade; body text is not set in gold.
- The look is original. It MUST NOT reuse another comparison site's logo, colours, layout, icons or copy (see [vision-and-scope.md](vision-and-scope.md#1-product-vision)).
- Brand assets (logo, wordmark, exact palette, typefaces) are not defined yet (OQ-22). Typefaces must render Thai well; whether they are self-hosted or loaded from a font service is part of OQ-22.

## 2. Language

- UX-04 (MUST): Thai and English across every screen: navigation, labels, help text, validation messages, product content, tax explanations, disclaimers, empty states and error pages.
- UX-05 (MUST): API errors are shown in the user's language, chosen by the frontend from the error `code` and detail `code` values. The API's English `message` is never shown as-is.
- UX-06 (MUST): A language switch is always visible, keeps the user's inputs and current page, and the choice is remembered on the device. The default language for first visits is OQ-23.
- UX-07 (MUST): The language is part of the URL (for example `/th/...` and `/en/...`) so pages can be shared and indexed per language `[proposal; OQ-23]`. How i18n is implemented in Nuxt, including whether a module is added, is OQ-24.
- UX-08 (MUST): Numbers use the locale's formatting. Money shows as THB (฿) with two decimals where needed. Tax years show both eras, for example "ปีภาษี 2569 (2026)" in Thai and "Tax Year 2026 (B.E. 2569)" in English.

## 3. Screens

| Screen | Requirements |
|---|---|
| Home | Value proposition, entry points to comparison and the tax calculator with equal weight (D2), the four categories |
| Category input | Only the inputs the category's estimate needs; why each input is asked; no health questions (SEC-03) |
| Results | Plan cards with indicative premium, key coverage, eligibility, last-updated date; sort by premium or name; filter by provider; select plans to compare |
| Comparison | Side-by-side table aligned by shared coverage types; differences visible; exclusions and conditions included, not hidden; sticky plan headers on mobile |
| Product detail | Full coverage, exclusions, conditions, tax-eligibility note for 2026, sources with dates, revision date, contact channel, "See tax impact" |
| Tax calculator | Step-by-step input (income, existing deductions, premiums); results before/after and the difference; per-premium explanation; rule version; limitations |
| About / How it works | What SafeInSiam is and is not (not an insurer; broker status per SEC-02), how data is collected, how ordering works |
| Privacy notice, terms, disclaimers | Bilingual; versioned (SEC-07) |
| Admin (internal) | Sign-in, provider and product lists with status filters, product editor with both languages side by side, rate-table editor with validation, source linking, publish with change note, revision history and diff |

## 4. Interaction requirements

- UX-09 (MUST): In the tax calculator, premiums being added are visually separate from deductions the user already has, so "before" and "after" are clear.
- UX-10 (MUST): Indicative-premium and tax-estimate disclaimers sit next to the figures they qualify, not only in a footer, and stay visible on mobile.
- UX-11 (MUST): Ineligible or unavailable plans say why (for example "entry age 20–60").
- UX-12 (MUST): Forms validate inline, in the user's language, and keep entered values after an error.
- UX-13 (MUST): Mobile-first layout; works at 360 px wide; comparison tables scroll horizontally inside their container, not the page.
- UX-14 (MUST): Keyboard accessible, with visible focus, labelled inputs and semantic tables for comparisons.
- UX-15 (SHOULD): Comparison and calculator states can be shared by URL without putting personal data in the URL (only category, plan slugs and non-identifying inputs; income never) — subject to OQ-06.
- UX-16 (MUST): No dark patterns: no countdowns, false scarcity, pre-ticked consent boxes, or "recommended" badges without a disclosed basis.
- UX-17 (MUST): When a deduction depends on a spouse's or parent's circumstances (TAX-16), the calculator asks the condition question directly with three choices (yes, no, don't know) and never pre-selects an answer. Results follow TAX-16: "no" shows the deduction as not applied because the condition is not met; "don't know" or no answer shows it as not applied because information is missing, naming what is missing; "yes" shows it as applied "based on your answer", never as verified.
