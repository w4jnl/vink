// Renders the design system's bundle.js for the props in
// testdata/golden/props.json and writes one golden file per case. Run with
// `make golden`; golden_test.go compares the Go partials against the files
// and, when node is available, checks the files are still fresh.
import { readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import vm from 'node:vm';
import path from 'node:path';

const here = path.dirname(new URL(import.meta.url).pathname);
const bundle = readFileSync(path.join(here, '..', '..', '..', 'docs', 'design-system', 'components', 'bundle.js'), 'utf8');
const props = JSON.parse(readFileSync(path.join(here, 'testdata', 'golden', 'props.json'), 'utf8'));
const outDir = process.argv[2] || path.join(here, 'testdata', 'golden');

const window = { Vink: {} };
vm.runInNewContext(bundle, { window, document: undefined, navigator: undefined, setTimeout });
const V = window.Vink;

for (const c of props) {
  const html = V[c.component](c.props);
  writeFileSync(path.join(outDir, c.name + '.html'), html);
}
console.log(`wrote ${props.length} golden files to ${outDir}`);
