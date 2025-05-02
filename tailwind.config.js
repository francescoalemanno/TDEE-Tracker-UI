/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./**/*.templ", "./**/*.html", "*.go", "./**/*.go"],
  includeLanguages: { templ: "html", go: "html" },
};