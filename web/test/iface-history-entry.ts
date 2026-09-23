/**
 * One bundle holding the panel AND the dialog it registers into.
 *
 * The extras registry inside `resource.ts` is private, which is correct — it is
 * not an API. So the test drives the REAL path instead of reaching into it:
 * this entry puts both modules in one bundle, so `registerExtra` and the
 * dialog's lookup are the same map, and a panel registered under the wrong
 * resource key fails the test rather than passing a direct call to `render`.
 *
 * Not a `*.test.ts`, so the runner's glob leaves it alone.
 */
export { initInterfaceHistory } from '../src/pages/interfaces-history';
export { openResource } from '../src/resource';
