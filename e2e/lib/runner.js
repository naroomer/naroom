// lib/runner.js — minimal test runner
export class Runner {
  constructor(name) {
    this.name = name;
    this.passed = 0;
    this.failed = 0;
    this.errors = [];
  }

  async run(label, fn) {
    try {
      await fn();
      console.log(`  ✓ ${label}`);
      this.passed++;
    } catch(e) {
      console.error(`  ✗ ${label}`);
      console.error(`    ${e.message}`);
      this.failed++;
      this.errors.push({ label, error: e.message });
    }
  }

  summary() {
    const total = this.passed + this.failed;
    console.log(`\n  ${this.name}: ${this.passed}/${total} passed`);
    if (this.failed > 0) {
      for (const { label, error } of this.errors) {
        console.error(`    FAILED: ${label}\n      ${error}`);
      }
      return false;
    }
    return true;
  }
}
