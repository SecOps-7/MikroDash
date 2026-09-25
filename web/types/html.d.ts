// The markup is imported as text, not parsed at build time. It is
// authored in web/src/ui/, one file per page.
declare module '*.html' {
  const content: string;
  export default content;
}
