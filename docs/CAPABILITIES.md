# Upstream capability matrix — Volkswagen Group EU Data Act

This file records **what is verified** about acquiring vehicle data from the Volkswagen Group EU Data Act portal, the
evidence for each claim, and what remains unverified. It is the source of truth for every acquisition feature in
WagenPulse. Update it only with evidence. Never record a VIN, an account identifier or a telemetry value here.

Last reviewed: 2026-11-02.

## Summary

| Capability | Status | Evidence | WagenPulse behaviour |
|---|---|---|---|
| Official, documented machine API (REST/OAuth for owners or third parties) | **Not found** | Portal pages refuse automated fetches (HTTP 403, 2026-11-02); no developer portal, API reference or OAuth scope documentation located; community projects describe the backend as undocumented (E3–E6) | No feature claims an official API |
| Owner downloads deliveries in a browser (ZIP) | **Verified** | 17 real ZIPs supplied by the project owner (E1) | File import (upload, CLI, API) |
| Continuous ("partial") delivery file format | **Verified** for the flat layout, **documented by community** for the dotted (MEB) layout | E1, E4–E6 | Parser `vwdataact.continuous.v1` |
| Historical ("all") one-time export format | **Unverified** | No sample available; dictionary implies a different, nested multi-system structure (E2) | Archives that do not match a verified structure are stored as `unrecognized` with a structural fingerprint and can be reprocessed when a parser exists; no values are invented |
| Automated download via the portal's web backend (`/proxy_api/euda-apim/…`) | **Unofficial, community-observed**, not live-verified by this project | E3–E6, `../cupra/internal/cupra/eudataact` | Opt-in source kind `vw_data_act_portal`, labelled "unofficial portal interface", disabled until configured |
| Login to the portal by script (VW Identity form replay) | **Unofficial, community-observed** | E4–E6 | Supported as `auth_mode = password`, opt-in |
| Login with a browser-captured portal session cookie | **Unofficial, community-observed** | E5, `../cupra` | Supported as `auth_mode = session_cookie` |
| Creating or renewing the portal data request (consent) | **Interactive only** | E4 (clients "cannot create or fix portal requests") | Never automated. The source shows `no_active_request` and links the user to the portal |
| Real-time / push / streaming | **Not available** | E3, E4 (≈15-minute delivery cadence at best) | Polling no faster than the configured interval (min 15 min) |
| GPS / location history | **Not present in continuous data** per community clients (E5); not observed in E1 | E1, E5 | Nothing is assumed |

## Evidence

### E1 — real continuous deliveries (owner-supplied, 2026-11-02)

17 ZIP files downloaded by the owner of a CUPRA PHEV (flat layout) from the portal. Observations (no values recorded):

- All 17 files are **byte-identical** (same SHA-256) although their file names carry 15 different timestamps. Each
  contains exactly one member named `<VIN>_<yyyymmddhhmmss>.json`; the member's timestamp is the same in all 17.
  Consequence: deduplication must be by content hash, and the ZIP file name is not a trustworthy delivery time.
- JSON root: `{"vin": string, "user_id": string, "Data": [ ... ]}`.
- 64 `Data` entries with keys `key` (UUID, version 3), `dataFieldName` (string), `value` (string; JSON `null` in 2
  entries; absent in 2 entries), `timestampUtc` (ISO 8601 UTC with milliseconds, `YYYY-MM-DDThh:mm:ss.sssZ`; absent in
  1 entry).
- Value shapes: 56 integer strings, 3 decimal strings, 1 boolean string, 1 empty string, 1 other short string, 2 null.
- 64 distinct keys and 64 distinct field names; the entries carry 3 distinct timestamps (a delivery is a merge of
  values captured at different times, not one snapshot).
- 62 of 64 keys appear in the Continuous dictionary (E2); 2 keys are absent from both dictionaries. Unknown keys must
  therefore be accepted and stored as signals with `origin = observed`.
- `user_id` is an upstream account identifier. WagenPulse treats it as sensitive and does not store it.

### E2 — official dictionaries (owner-supplied PDFs)

| Document | Version | Date | Pages | Unique keys | Columns |
|---|---|---|---|---|---|
| EU Data Act — List of Continuous Data | 1.0.0 | 2025-09-11 | 212 | 1141 | Key, Data Point Name, Description, Measurement Unit, Tech. Data Type, Data Cluster |
| EU Data Act — List of Historical Data | 1.0.0 | 2025-09-11 | 797 | 5140 | Key, Data Point Name, Description, Measurement Unit, Tech. Data Type |

- File names `251022_01_SVK_DataDictionary_V4.0_*` indicate portal label "V4.0". Community sources reference a later
  "V5.0" continuous dictionary (document 1.0.5, 2026-02-25, 1142 entries); WagenPulse has not seen it.
- Both documents state that data point names may repeat ("an extract from different systems").
- Key sets of the two documents are disjoint.
- Historical data point names are nested system paths (`VTS.vehicles.[*].registrationCountry`,
  `RS.actionSessions.[*].relatedJobId`, `BMS_CMC_Zellspannung_*`, …) with SQL-style types (`bigint`,
  `character varying(2)`), so the historical export is structurally different from the continuous delivery.
- Units are free text (`min`, `km`, `kWh`, `Minute`, `'-`, blank). Data types are free text (`int`, `string`, `float`,
  `bool`, `unsignedInt`, `bigint`, …).
- A dictionary is metadata, not an API contract: it documents what might be delivered, not whether a given vehicle
  delivers it.

### E3 — portal

- `https://eu-data-act.drivesomethinggreater.com/…` returned HTTP 403 to automated fetches on 2026-11-02.
- The portal is the owner-facing EU Data Act channel for VW, Audi, Škoda, SEAT, CUPRA, VW Commercial and Bentley
  (community sources; Porsche is not on the portal).

### E4 — HA_VAG-EU-Data-Act (github.com/TommiG1/HA_VAG-EU-Data-Act)

- Login: OIDC authorization-code URL on `identity.vwgroup.io` with the portal's public per-brand `client_id`, scope
  `openid cars profile`, `state = {country}__{language}__{BRAND}`, `redirect_uri = <portal>/login`; the client scrapes
  `hmac`, `relayState`, `_csrf` from the sign-in HTML and POSTs email, then password; the portal exchanges the code and
  sets session cookies.
- Data: `GET /proxy_api/euda-apim/datarequest/vehicles/{vin}/metadata/partial` → `Identifier`;
  `GET …/datadelivery/vehicles/{vin}/{identifier}/list` (header `type: partial`) → `[{name, createdOn, size}]`;
  `GET …/download` (headers `filename`, `type`) → ZIP. `*_no_content_found.zip` means no vehicle data.
- Only one customised data request can be active; a pending one-time export blocks a new continuous request for up to
  24 h; requests must be renewed about every 12 months; first data can take hours to days.
- Treats 429/5xx as temporary with a 5/15/30-minute back-off ladder; identifies itself with a product User-Agent.

### E5 — CarConnectivity connector (github.com/mikrohard/CarConnectivity-connector-vw-eu-data-act)

- Same endpoints; `metadata/{partial|all}` selects continuous vs on-demand historical export on the same delivery
  endpoints.
- HTTP 429 observed on list and download; honours `Retry-After`.
- October 2026: VW asked community clients to identify with a product token instead of a browser User-Agent and to
  stop "priming" GET requests (millions of requests).
- Notes that a delivery "is a merge, not a moment" and that dotted (MEB) layouts carry only report-level capture times.

### E6 — evcc (docs.evcc.io/en/vehicles/volkswagen-eu-data-act) and ../cupra

- evcc requires the user to create a continuous 15-minute request in the browser; without it the portal returns no
  data.
- `../cupra/internal/cupra/eudataact` (same author as WagenPulse) implements the same flow plus a browser-captured
  session cookie mode.

### Verification performed by WagenPulse

| Date | What | Result |
|---|---|---|
| 2026-11-02 | `internal/ingest` `TestRealSamples` over the 17 owner-supplied ZIPs (`WAGENPULSE_REAL_SAMPLES` glob; data stays outside the repository) with the bundled dictionaries | 17 files → 1 stored delivery + 16 byte-identical duplicates; 64 observations accepted; 62 described by the Continuous dictionary, 2 stored as `observed` signals; 0 rejected, 0 warnings |
| 2026-11-02 | `tools/dictgen` over both dictionary PDFs | Continuous: 1141 entries (v1.0.0, 2025-09-11); Historical: 5140 entries — matching the unique key counts of the PDFs |
| 2026-11-02 | Collector against `fakeportal` (synthetic imitation of E4–E6 behaviour) | cookie and password (Auth0 page) modes, checkpoints, `no_content`, 429/Retry-After, expired session, bot challenge refusal — **not** a live verification |
| 2026-11-02 | Live owner test, password mode: the collector stopped with "bot challenge" before submitting credentials | **Our bug.** The check matched captcha scripts that Auth0's sign-in page always loads. The E4 client (HA_VAG-EU-Data-Act) submits the form without such a pre-check. A challenge is now detected only from a captcha the form actually demands, a challenge page served instead of the form, or a rejected submission. Retest pending |
| 2026-11-02 | Live owner test after that fix (CUPRA, password mode) | **Verified by the owner:** scripted login (Auth0), authorization and collection of a continuous delivery into WagenPulse. One account, one vehicle; session lifetime not yet observed |

## Unresolved gaps and what would close them

| Gap | Evidence needed |
|---|---|
| Historical export structure | One real historical export archive (sanitized structure is enough) |
| Dotted (MEB) continuous layout | One real MEB delivery |
| Live verification of the unofficial collector | An operator running the collector against their own account; results recorded here without identifiers |
| Official API | A VW-published API reference, OAuth client registration process and terms for data recipients |
| V5.0 dictionary | The newer dictionary PDF; import it with `wagenpulse dictionary import` |

## Dictionary errata

The published Continuous Data dictionary (v1.0.0, 2025-09-11) contains copy-paste errors in some code descriptions.
WagenPulse corrects only those that are unambiguous from sibling signals (`dictionary.EnumFor`). Stored values are
never changed; only their decoding is.

| Signals | Published description | Decoded as | Basis |
|---|---|---|---|
| `open_state_front_right_door`, `open_state_rear_right_door` | "locked (2) or unlocked (3)" | open (2), closed (3) | every other `open_state_*` signal |
| `safe_state_front_engine_bonnet`, `safe_state_tailgate` | lists unsupported (0), invalid (1), unsafe (3); no code 2 | adds safe (2) | every other `safe_state_*` signal |

The tyre-pressure unit is published as "10kPA / Bar / PSI / kPA". It is ambiguous, so values are shown as reported
and never converted.
