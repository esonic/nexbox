# Third-Party Notices

NexBox includes or distributes third-party software under its own licenses:

- Border Beam, MIT; license at `web/src/components/ui/border-beam/LICENSE`.
- Chart.js 4.4.1, MIT; license at `web/public/vendor/chart.LICENSE.txt`.
- D3 7.8.5, ISC; license at `web/public/vendor/d3.LICENSE.txt`.
- TopoJSON 3.0.2, BSD-3-Clause; license at
  `web/public/vendor/topojson.LICENSE.txt`.
- Mermaid 12.2.0, MIT; loaded on demand from the version-pinned jsDelivr URL
  declared in `web/src/components/markdown/MarkdownRenderer.tsx`.

Dependencies installed through Go modules and npm retain their upstream
copyright and license terms. See `server/go.mod`, `server/go.sum`,
`web/package.json`, and `web/package-lock.json` for the exact dependency set.
