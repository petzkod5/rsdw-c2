// Contrast and colour-blind checks for the tokens in design-brief.md. Run: node palette-check.cjs
// WCAG 2.x contrast checker for the proposed tokens.
const lin = (c) => { c /= 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
const lum = (hex) => { const n = parseInt(hex.slice(1), 16); return 0.2126 * lin((n >> 16) & 255) + 0.7152 * lin((n >> 8) & 255) + 0.0722 * lin(n & 255); };
const cr = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };

const T = {
  bg: '#111210', s1: '#171815', s2: '#1E1F1B', s3: '#282923', line: '#2C2D28', lineStrong: '#6D6E64',
  text: '#ECEAE3', text2: '#B7B5AB', text3: '#8F8D83',
  bone: '#E8DFC6', boneInk: '#17150E',
  online: '#4CC38A', offline: '#F0645C', stopped: '#A8A89E', starting: '#6EA8FE', attention: '#E8B14A',
  dangerFill: '#B83A34', white: '#FFFFFF',
};
const pairs = [
  ['text on bg', T.text, T.bg, 7], ['text on s1', T.text, T.s1, 7], ['text on s2', T.text, T.s2, 7], ['text on s3 (selected)', T.text, T.s3, 7],
  ['text2 on bg', T.text2, T.bg, 4.5], ['text2 on s2', T.text2, T.s2, 4.5], ['text2 on s3', T.text2, T.s3, 4.5],
  ['text3 on bg', T.text3, T.bg, 4.5], ['text3 on s2', T.text3, T.s2, 4.5],
  ['boneInk on bone (primary button)', T.boneInk, T.bone, 7],
  ['bone link on bg', T.bone, T.bg, 7],
  ['lineStrong vs s1 (control border, 3:1)', T.lineStrong, T.s1, 3], ['lineStrong vs s2 (input border)', T.lineStrong, T.s2, 3],
  ['line vs bg (decorative hairline)', T.line, T.bg, 1.2],
  ['online dot vs bg (3:1)', T.online, T.bg, 3], ['online dot vs s2', T.online, T.s2, 3],
  ['offline dot vs bg', T.offline, T.bg, 3], ['offline dot vs s2', T.offline, T.s2, 3],
  ['stopped glyph vs bg', T.stopped, T.bg, 3], ['stopped glyph vs s2', T.stopped, T.s2, 3],
  ['starting vs bg', T.starting, T.bg, 3], ['attention vs bg', T.attention, T.bg, 3],
  ['online as TEXT on bg (4.5)', T.online, T.bg, 4.5], ['offline as TEXT on bg (4.5)', T.offline, T.bg, 4.5], ['attention as TEXT on bg', T.attention, T.bg, 4.5], ['starting as TEXT on bg', T.starting, T.bg, 4.5],
  ['white on dangerFill', T.white, T.dangerFill, 4.5],
  ['dangerFill vs bg (non-text 3:1)', T.dangerFill, T.bg, 3],
];
let fail = 0;
for (const [name, fg, bg, min] of pairs) {
  const r = cr(fg, bg); const ok = r >= min; if (!ok) fail++;
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${r.toFixed(2).padStart(6)}:1  (need ${min})  ${name}  ${fg} on ${bg}`);
}
console.log(fail ? `\n${fail} failing pairs` : '\nall pairs pass');
// colour-blind separation check: deuteranopia-simulated distance between status dots (Machado 2009, severity 1.0)
const toLin = (hex) => { const n = parseInt(hex.slice(1), 16); return [lin((n >> 16) & 255), lin((n >> 8) & 255), lin(n & 255)]; };
const sim = { deut: [[0.367322, 0.860646, -0.227968], [0.280085, 0.672501, 0.047413], [-0.011820, 0.042940, 0.968881]], prot: [[0.152286, 1.052583, -0.204868], [0.114503, 0.786281, 0.099216], [-0.003882, -0.048116, 1.051998]] };
const mul = (m, v) => m.map((r) => r[0] * v[0] + r[1] * v[1] + r[2] * v[2]);
const labF = (t) => (t > 216 / 24389 ? Math.cbrt(t) : (24389 / 27 * t + 16) / 116);
const toLab = (rgb) => { const [r, g, b] = rgb; const X = (0.4124 * r + 0.3576 * g + 0.1805 * b) / 0.95047, Y = 0.2126 * r + 0.7152 * g + 0.0722 * b, Z = (0.0193 * r + 0.1192 * g + 0.9505 * b) / 1.08883; const fx = labF(X), fy = labF(Y), fz = labF(Z); return [116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz)]; };
const dE = (a, b) => Math.hypot(...a.map((v, i) => v - b[i]));
const names = ['online', 'offline', 'stopped', 'starting', 'attention'];
for (const k of ['deut', 'prot']) {
  let min = 1e9, pair = '';
  for (let i = 0; i < names.length; i++) for (let j = i + 1; j < names.length; j++) {
    const d = dE(toLab(mul(sim[k], toLin(T[names[i]]))), toLab(mul(sim[k], toLin(T[names[j]]))));
    if (d < min) { min = d; pair = `${names[i]}~${names[j]}`; }
  }
  console.log(`${k} simulated: closest status pair ${pair} dE76=${min.toFixed(1)} (hue alone insufficient if < ~25; shape+label carry the meaning anyway)`);
}
