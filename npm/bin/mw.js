#!/usr/bin/env node
// Runs the `mw` binary this package installed, fetching it first when the
// install skipped its script.
'use strict';
const fs = require('fs');
const { spawnSync } = require('child_process');
const { install, exe } = require('../install.js');

(async () => {
  if (!fs.existsSync(exe)) {
    try {
      await install();
    } catch (e) {
      console.error(`mw: could not download the binary: ${e.message}`);
      process.exit(1);
    }
  }
  const r = spawnSync(exe, process.argv.slice(2), { stdio: 'inherit' });
  if (r.error) {
    console.error(`mw: ${r.error.message}`);
    process.exit(1);
  }
  process.exit(r.status ?? 1);
})();
