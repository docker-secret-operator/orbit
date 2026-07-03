import { site } from "@/lib/site";
import Terminal from "./Terminal";
import CopyButton from "./CopyButton";
import { GitHubIcon, BookIcon } from "./icons";
import styles from "./Hero.module.css";

export default function Hero() {
  return (
    <section className={styles.hero} aria-labelledby="hero-title">
      <div className={`container ${styles.grid}`}>
        <div className={styles.copy}>
          <p className={styles.badge}>
            <span className={styles.dot} aria-hidden />
            Docker CLI plugin · MIT licensed
          </p>

          <h1 id="hero-title" className={styles.title}>
            Zero-downtime deploys for Docker&nbsp;Compose.
          </h1>

          <p className={styles.lead}>
            Orbit is a single binary that gives your existing Compose stack
            health-aware rolling updates — so recreating a container never drops
            a connection.
          </p>

          <p className={styles.sub}>
            No Kubernetes. No Traefik or nginx to run. No rewrite of your{" "}
            <code>docker-compose.yml</code>. Drop the binary next to your stack
            and the host port never goes dark again — not during deploys, not
            during restarts.
          </p>

          <div className={styles.installRow}>
            <code className={styles.install}>{site.installScript}</code>
            <CopyButton value={site.installScript} />
          </div>

          <div className={styles.actions}>
            <a
              className="btn btn-primary"
              href={site.repo}
              target="_blank"
              rel="noreferrer"
            >
              <GitHubIcon width={18} height={18} />
              View on GitHub
            </a>
            <a className="btn" href={site.docs}>
              <BookIcon width={18} height={18} />
              Read the docs
            </a>
          </div>

          <dl className={styles.status}>
            <div>
              <dt>License</dt>
              <dd>{site.status.license}</dd>
            </div>
            <div>
              <dt>Distribution</dt>
              <dd>Docker CLI plugin</dd>
            </div>
            <div>
              <dt>Platforms</dt>
              <dd>
                {site.status.platforms} · {site.status.arch}
              </dd>
            </div>
            <div>
              <dt>Status</dt>
              <dd>
                <a href={site.links.reliabilityReport} className={styles.statusLink}>
                  {site.status.maturity}
                </a>
              </dd>
            </div>
          </dl>
        </div>

        <div className={styles.demo}>
          <Terminal
            title="deploy v2"
            lines={[
              { k: "cmd", t: "docker orbit rollout web" },
              { k: "blank" },
              { k: "out", t: "→ starting new backend    web  (2.0.0)" },
              { k: "ok", t: "✓ health check passed     web  (2.0.0)" },
              { k: "out", t: "→ registered with proxy   POST /backends" },
              { k: "out", t: "→ draining old backend    web  (1.0.0)" },
              { k: "blank" },
              { k: "ok", t: "✓ rollout complete        0 connections dropped" },
            ]}
          />
          <p className={styles.demoNote}>
            The proxy owns the host port permanently. Your clients see nothing.
          </p>
        </div>
      </div>
    </section>
  );
}
