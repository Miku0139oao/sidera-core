#!/usr/bin/env node
import fs from "node:fs";

function fail(message) {
  console.error(`release workflow check: ${message}`);
  process.exit(1);
}

const build = fs.readFileSync(".github/workflows/build.yml", "utf8");
const docker = fs.readFileSync(".github/workflows/docker.yml", "utf8");

function jobBlock(source, name) {
  const startMatch = new RegExp(`^  ${name}:\\r?$`, "m").exec(source);
  if (!startMatch) fail(`job ${name} is missing`);
  const bodyStart = source.indexOf("\n", startMatch.index) + 1;
  const remainder = source.slice(bodyStart);
  const nextJob = /^  [A-Za-z0-9_]+:\r?$/m.exec(remainder);
  return nextJob ? remainder.slice(0, nextJob.index) : remainder;
}

function requireNeed(source, job, dependency) {
  const block = jobBlock(source, job);
  const needs = block.match(/^    needs:\r?\n((?:      - [^\r\n]+\r?\n)+)/m)?.[1] ?? "";
  if (!new RegExp(`^      - ${dependency}\\r?$`, "m").test(needs)) {
    fail(`${job} does not directly require ${dependency}`);
  }
}

requireNeed(build, "publish_provenance", "release_gate");
requireNeed(build, "publish_android", "publish_provenance");
requireNeed(build, "build_apple_app_store", "publish_provenance");
requireNeed(build, "upload", "release_gate");
if (!jobBlock(build, "upload").includes("inputs.build == 'All'")) {
  fail("GitHub release upload is not restricted to a complete build inventory");
}
for (const job of ["build_docker", "merge"]) {
  const block = jobBlock(docker, job);
  if (!block.includes("ref: ${{ github.sha }}") || !block.includes("Verify immutable source")) {
    fail(`${job} does not checkout and verify the immutable workflow SHA`);
  }
}
for (const script of [".github/build_alpine_apk.sh", ".github/build_openwrt_apk.sh"]) {
  const source = fs.readFileSync(script, "utf8");
  for (const required of ["NOTICE.md", "UPSTREAM.md", "release/licenses", "sidera.buildinfo"]) {
    if (!source.includes(required)) fail(`${script} omits ${required}`);
  }
}
const dockerfile = fs.readFileSync("Dockerfile.binary", "utf8");
for (const required of ["NOTICE.md", "UPSTREAM.md", "release/licenses/", "sidera.buildinfo"]) {
  if (!dockerfile.includes(required)) fail(`Dockerfile.binary omits ${required}`);
}
console.log("release workflow publication DAG and payload checks passed");
