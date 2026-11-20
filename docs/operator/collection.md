# Getting data in

WagenPulse never creates data requests or consents on the portal. Create the **continuous data request** for each
vehicle yourself, in a browser, on your brand's EU Data Act portal. The portal then produces a delivery file roughly
every 15 minutes while the car sends data. Requests renew about every 12 months, and only one is active at a time.

## Uploading deliveries (official path)

Download delivery files from the portal and drop them on **Imports**. ZIP and JSON are accepted, many files at once.

- **Duplicates**: files are identified by SHA-256 content hash. A file you already imported is skipped, whatever its
  name. The portal names files arbitrarily, so the same content often arrives under different names.
- **VIN matching**: each delivery's VIN must match the selected vehicle. If you leave the vehicle empty, it must
  match one of your vehicles.
- **Event policy**:
  - *Store only* (the default for uploads) creates no events.
  - *Preview* counts what rules would have fired.
  - *Publish* delivers events marked `backfill: true`, only to routes that accept backfill.
- **Unknown signals**: keys missing from the bundled dictionaries are stored as received, under the `observed`
  origin. No units or types are invented.
- **Unrecognized files**: a file whose structure is not a known delivery format is kept as `unrecognized` and shown
  with its structure fingerprint. You can reprocess it later from **Imports → Raw deliveries**. The historical export
  format has not been seen yet, so historical files land here.
- **Limits**: 50 MiB per upload, 200 MiB extracted, 1000 archive entries and a 100:1 compression ratio by default.
  Nested archives, symlinks and path traversal are refused.

## Portal collector (unofficial interface)

A **source** downloads new deliveries on a schedule. It uses the portal's own web backend, which is **undocumented**.
Community clients observe that interface, and it may change or block automated access at any time. WagenPulse:

- identifies itself honestly (`User-Agent: WagenPulse (+https://github.com/aurimasniekis/go-wagenpulse)`) and sends
  no priming requests;
- polls no faster than every 15 minutes, with a per-source rate limit and exponential backoff that honours
  `Retry-After`;
- stops and asks for help whenever the portal shows a CAPTCHA, new terms or a consent prompt. It never bypasses them.

The collector has been tested only against a synthetic imitation of the portal. **Live collection is unverified.**
If you try it, please report the outcome without VINs or values; see [../CAPABILITIES.md](../CAPABILITIES.md).

### Authentication modes

- **Browser session cookie**:
  1. Sign in to the portal in a browser.
  2. Open the developer tools, then Network, and select a request to the portal.
  3. Copy the `Cookie` request header value into the source's secret.

  The session expires with the portal session. The source then reports `reauthorization_required` and you paste a
  fresh cookie.
- **Scripted login** (email and password): WagenPulse signs in the way the portal's login page does. Some brands need
  the *portal brand key* from the login URL.

Secrets can be stored encrypted or referenced externally (`env:` / `file:`); see
[configuration.md](configuration.md#secrets).

### Source states

| State | Meaning | What to do |
|---|---|---|
| `configured` | Not checked yet | Wait for the first run, or use **Collect now** |
| `authorized` | Signed in, active data request found | Nothing |
| `reauthorization_required` | Session expired, password changed, or a CAPTCHA, terms or consent prompt | Sign in on the portal in a browser, resolve any prompt, then update the secret |
| `no_active_request` | No continuous data request for this account | Create one on the portal in a browser, then use **Collect now** (password mode does not sign in again by itself until you do) |
| `rate_limited` | The portal asked us to slow down | Nothing: collection resumes after `retry_after` |
| `unavailable` | Portal errors | Nothing: retried with backoff |
| `unsupported` | Brand or account not supported by the interface | Use uploads |
| `error` | Unexpected failure | Check the reason, and find the server log by correlation ID |

Collected files are imported with the source's event policy: new data defaults to *publish*, historical data to
*store only*.
