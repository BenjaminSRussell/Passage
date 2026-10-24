import { readFile, readdir } from "node:fs/promises";
import { join } from "node:path";
import { assertBackwardCompatible, type JsonSchema } from "./schemaCompatibility.js";

const [baselineDirectory, currentDirectory] = process.argv.slice(2);
if (!baselineDirectory || !currentDirectory) {
  throw new Error("usage: checkSchemas <baseline-directory> <current-directory>");
}

const baselineFiles = (await readdir(baselineDirectory))
  .filter((file) => file.endsWith(".json"))
  .sort();
if (baselineFiles.length === 0) {
  throw new Error("schema baseline is empty");
}

for (const file of baselineFiles) {
  const [baseline, current] = await Promise.all([
    loadSchema(join(baselineDirectory, file)),
    loadSchema(join(currentDirectory, file)),
  ]);
  try {
    assertBackwardCompatible(baseline, current);
  } catch (error) {
    throw new Error(`${file} is not backward compatible`, { cause: error });
  }
}

console.log(`schema compatibility: ${baselineFiles.length} artifact(s) passed`);

async function loadSchema(path: string): Promise<JsonSchema> {
  return JSON.parse(await readFile(path, "utf8")) as JsonSchema;
}
