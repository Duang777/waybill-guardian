import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";
import subsetFont from "subset-font";

const webDir = fileURLToPath(new URL("..", import.meta.url));
const repoDir = fileURLToPath(new URL("../..", import.meta.url));
const outputDir = join(webDir, "src", "assets", "fonts");
const corpusRoots = [
  join(webDir, "src"),
  join(repoDir, "cmd"),
  join(repoDir, "data"),
  join(repoDir, "internal", "agent"),
  join(repoDir, "internal", "guardian"),
  join(repoDir, "internal", "platform"),
  join(repoDir, "internal", "tools"),
];
const sourceExtensions = new Set([".csv", ".go", ".json", ".ts", ".tsx"]);
const weights = [400, 500, 700];
const baseCorpus =
  " !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~";

const sourceFiles = (
  await Promise.all(corpusRoots.map((root) => collectSourceFiles(root)))
).flat();
const sourceText = await Promise.all(
  sourceFiles.map((path) => readFile(path, "utf8")),
);
const corpus = [...new Set(`${baseCorpus}${sourceText.join("")}`)].join("");

await mkdir(outputDir, { recursive: true });
let totalBytes = 0;
for (const weight of weights) {
  const sourcePath = join(
    webDir,
    "node_modules",
    "@fontsource",
    "noto-sans-sc",
    "files",
    `noto-sans-sc-chinese-simplified-${weight}-normal.woff2`,
  );
  const source = await readFile(sourcePath);
  const subset = await subsetFont(source, corpus, {
    targetFormat: "woff2",
    noHinting: true,
    preserveNameIds: [0, 1, 2, 3, 4, 5, 6],
  });
  const outputPath = join(outputDir, `wg-sans-sc-${weight}.woff2`);
  await writeFile(outputPath, subset);
  totalBytes += subset.byteLength;
  console.log(`${weight}: ${subset.byteLength} bytes`);
}

if (totalBytes >= 320_000) {
  throw new Error(
    `generated font subset is ${totalBytes} bytes, expected less than 320000`,
  );
}
console.log(
  `generated ${corpus.length} glyphs from ${sourceFiles.length} runtime files in ${totalBytes} bytes`,
);

async function collectSourceFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nested = await Promise.all(
    entries.map(async (entry) => {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) {
        return collectSourceFiles(path);
      }
      return sourceExtensions.has(extname(path)) ? [path] : [];
    }),
  );
  return nested.flat();
}
