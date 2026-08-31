// The extracted markup is imported as text, not parsed at build time. It is
// verbatim from the live app (tools/extract-ui.js) and must stay that way.
declare module '*.html' {
  const content: string;
  export default content;
}
