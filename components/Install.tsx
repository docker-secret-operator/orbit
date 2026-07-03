"use client";

import { useState } from "react";
import { site } from "@/lib/site";
import type { Line } from "./Terminal";
import Terminal from "./Terminal";
import CopyButton from "./CopyButton";
import styles from "./Install.module.css";

type Tab = {
  id: string;
  label: string;
  copy: string;
  lines: Line[];
};

const tabs: Tab[] = [
  {
    id: "script",
    label: "Linux & macOS",
    copy: "curl -fsSL https://raw.githubusercontent.com/docker-secret-operator/orbit/main/install.sh | bash",
    lines: [
      { k: "comment", t: "# detects OS/arch, verifies SHA256, installs the plugin" },
      { k: "cmd", t: "curl -fsSL https://raw.githubusercontent.com/…/install.sh | bash" },
      { k: "ok", t: "✓ installed docker-orbit as a Docker CLI plugin" },
    ],
  },
  {
    id: "packages",
    label: "Linux packages",
    copy: "sudo dpkg -i docker-orbit_*_amd64.deb",
    lines: [
      { k: "comment", t: "# grab the .deb or .rpm from the releases page" },
      { k: "cmd", t: "sudo dpkg -i docker-orbit_*_amd64.deb" },
      { k: "comment", t: "# or, on rpm-based distros" },
      { k: "cmd", t: "sudo rpm -i docker-orbit_*_x86_64.rpm" },
    ],
  },
  {
    id: "source",
    label: "From source",
    copy: "git clone https://github.com/docker-secret-operator/orbit.git && cd orbit && make install-plugin",
    lines: [
      { k: "cmd", t: "git clone https://github.com/docker-secret-operator/orbit.git" },
      { k: "cmd", t: "cd orbit && make install-plugin" },
      { k: "comment", t: "# or: go install …/cmd/docker-orbit@latest" },
    ],
  },
  {
    id: "verify",
    label: "Verify",
    copy: "docker orbit doctor",
    lines: [
      { k: "cmd", t: "docker orbit version" },
      { k: "cmd", t: "docker orbit doctor" },
      { k: "ok", t: "✓ plugin installed · Docker reachable · ready" },
    ],
  },
];

export default function Install() {
  const [active, setActive] = useState(tabs[0].id);
  const current = tabs.find((t) => t.id === active) ?? tabs[0];

  return (
    <section className="section" id="install" aria-labelledby="install-title">
      <div className="container">
        <p className="eyebrow">Install</p>
        <h2 id="install-title" className="section-title">
          One binary. No Go toolchain required.
        </h2>
        <p className="section-lead">
          Orbit installs as a native <code>docker</code> plugin on Linux and
          macOS, amd64 or arm64.
        </p>

        <div className={styles.panel}>
          <div
            className={styles.tabs}
            role="tablist"
            aria-label="Installation method"
          >
            {tabs.map((t) => (
              <button
                key={t.id}
                role="tab"
                id={`tab-${t.id}`}
                aria-selected={active === t.id}
                aria-controls={`panel-${t.id}`}
                tabIndex={active === t.id ? 0 : -1}
                className={`${styles.tab} ${
                  active === t.id ? styles.tabActive : ""
                }`}
                onClick={() => setActive(t.id)}
              >
                {t.label}
              </button>
            ))}
          </div>

          <div
            role="tabpanel"
            id={`panel-${current.id}`}
            aria-labelledby={`tab-${current.id}`}
            className={styles.body}
          >
            <Terminal title={current.label} lines={current.lines} />
            <div className={styles.copyRow}>
              <CopyButton value={current.copy} label="Copy install command" />
            </div>
          </div>
        </div>

        <nav className={styles.next} aria-label="Next steps">
          <span className={styles.nextLabel}>Where to next</span>
          <a href={site.links.installation}>Installation guide</a>
          <a href={site.links.deploymentGuide}>Deployment guide</a>
          <a href={site.examples}>Examples</a>
          <a href={site.links.constitution}>Design &amp; guarantees</a>
          <a href={site.repo}>GitHub</a>
        </nav>
      </div>
    </section>
  );
}
