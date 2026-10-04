import { readdir, readFile } from "node:fs/promises";
import { extname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const webDir = fileURLToPath(new URL("..", import.meta.url));
const sourceDir = join(webDir, "src");
const themePath = join(sourceDir, "styles", "theme.css");
const errors = [];
const sourceFiles = await collectSourceFiles(sourceDir);

for (const path of sourceFiles) {
  if (path === themePath) {
    continue;
  }
  const source = await readFile(path, "utf8");
  const displayPath = relative(webDir, path);
  checkRawColors({ displayPath, source });
  if (path.endsWith(".css")) {
    checkCSSDeclarations({ displayPath, source });
  }
}

const theme = await readFile(themePath, "utf8");
checkRequiredImports(theme);
checkRequiredTokens(theme);
checkContrast(theme);

if (errors.length > 0) {
  console.error(errors.join("\n"));
  process.exit(1);
}

console.log(
  `design system check passed for ${sourceFiles.length} source files and 9 contrast pairs`,
);

async function collectSourceFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nested = await Promise.all(
    entries.map(async (entry) => {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) {
        return collectSourceFiles(path);
      }
      return [path];
    }),
  );
  return nested.flat().filter((path) =>
    [".css", ".ts", ".tsx"].includes(extname(path)),
  );
}

function checkRawColors({ displayPath, source }) {
  const pattern = /#[\da-f]{3,8}\b|(?:oklch|rgba?|hsla?)\(/gi;
  for (const match of source.matchAll(pattern)) {
    errors.push(
      `${displayPath}:${lineNumber(source, match.index)} raw color ${match[0]} must be a theme token`,
    );
  }
}

function checkCSSDeclarations({ displayPath, source }) {
  const declarations = source.matchAll(
    /(^|\n)\s*([-\w]+)\s*:\s*([^;{}]+);/g,
  );
  for (const match of declarations) {
    const property = match[2];
    const value = match[3].trim();
    const line = lineNumber(source, match.index);
    if (
      /^(gap|row-gap|column-gap|padding|padding-.+|margin|margin-.+)$/.test(
        property,
      ) &&
      /\d+px/.test(value)
    ) {
      errors.push(`${displayPath}:${line} ${property} must use a spacing token`);
    }
    if (
      property === "border-radius" &&
      value !== "0" &&
      value !== "inherit" &&
      !value.startsWith("var(")
    ) {
      errors.push(`${displayPath}:${line} border-radius must use a radius token`);
    }
    if (property === "font-family" && !value.startsWith("var(")) {
      errors.push(`${displayPath}:${line} font-family must use a font token`);
    }
    if (property === "box-shadow" && !value.startsWith("var(")) {
      errors.push(`${displayPath}:${line} box-shadow must use an elevation token`);
    }
    if (property === "transition" && /\ball\b/.test(value)) {
      errors.push(`${displayPath}:${line} transition: all is forbidden`);
    }
  }
}

function checkRequiredImports(theme) {
  const imports = [
    '@import "@fontsource-variable/jetbrains-mono/wght.css";',
    '@import "tailwindcss";',
    'font-family: "WG Sans SC";',
    'url("../assets/fonts/wg-sans-sc-400.woff2")',
    'url("../assets/fonts/wg-sans-sc-500.woff2")',
    'url("../assets/fonts/wg-sans-sc-700.woff2")',
  ];
  for (const required of imports) {
    if (!theme.includes(required)) {
      errors.push(`src/styles/theme.css is missing ${required}`);
    }
  }
}

function checkRequiredTokens(theme) {
  const required = [
    "--background",
    "--surface-1",
    "--surface-2",
    "--surface-3",
    "--foreground",
    "--muted-foreground",
    "--border",
    "--primary",
    "--success",
    "--danger",
    "--info",
    "--chart-1",
    "--chart-6",
    "--font-family-sans",
    "--font-family-mono",
    "--radius-xs",
    "--radius-lg",
    "--duration-press",
  ];
  for (const token of required) {
    if (!theme.includes(`${token}:`)) {
      errors.push(`src/styles/theme.css is missing ${token}`);
    }
  }
}

function checkContrast(theme) {
  const darkTheme = theme.slice(0, theme.indexOf(":root.light"));
  const pairs = [
    ["--background", "--foreground", 4.5],
    ["--surface-2", "--foreground", 4.5],
    ["--surface-3", "--foreground", 4.5],
    ["--primary", "--primary-foreground", 4.5],
    ["--danger", "--danger-foreground", 4.5],
    ["--primary-wash", "--primary-text", 4.5],
    ["--success-wash", "--success-text", 4.5],
    ["--danger-wash", "--danger-text", 4.5],
    ["--info-wash", "--info-text", 4.5],
  ];
  for (const [background, foreground, minimum] of pairs) {
    const backgroundColor = readOklchToken(darkTheme, background);
    const foregroundColor = readOklchToken(darkTheme, foreground);
    if (backgroundColor === null || foregroundColor === null) {
      errors.push(`cannot read contrast pair ${foreground} on ${background}`);
      continue;
    }
    const ratio = contrastRatio(backgroundColor, foregroundColor);
    if (ratio < minimum) {
      errors.push(
        `${foreground} on ${background} has ${ratio.toFixed(2)}:1 contrast, expected at least ${minimum}:1`,
      );
    }
  }
}

function readOklchToken(theme, token) {
  const escaped = token.replaceAll("-", "\\-");
  const match = new RegExp(
    `${escaped}:\\s*oklch\\(([\\d.]+)\\s+([\\d.]+)\\s+([\\d.]+)`,
  ).exec(theme);
  if (match === null) {
    return null;
  }
  return {
    lightness: Number(match[1]),
    chroma: Number(match[2]),
    hue: Number(match[3]),
  };
}

function contrastRatio(left, right) {
  const leftLuminance = relativeLuminance(left);
  const rightLuminance = relativeLuminance(right);
  const lighter = Math.max(leftLuminance, rightLuminance);
  const darker = Math.min(leftLuminance, rightLuminance);
  return (lighter + 0.05) / (darker + 0.05);
}

function relativeLuminance({ lightness, chroma, hue }) {
  const radians = (hue * Math.PI) / 180;
  const a = chroma * Math.cos(radians);
  const b = chroma * Math.sin(radians);
  const lRoot = lightness + 0.3963377774 * a + 0.2158037573 * b;
  const mRoot = lightness - 0.1055613458 * a - 0.0638541728 * b;
  const sRoot = lightness - 0.0894841775 * a - 1.291485548 * b;
  const l = lRoot ** 3;
  const m = mRoot ** 3;
  const s = sRoot ** 3;
  const red = clamp(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s);
  const green = clamp(
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
  );
  const blue = clamp(
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  );
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}

function clamp(value) {
  return Math.min(1, Math.max(0, value));
}

function lineNumber(source, index = 0) {
  return source.slice(0, index).split("\n").length;
}
