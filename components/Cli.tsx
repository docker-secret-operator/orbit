import { site } from "@/lib/site";
import Terminal from "./Terminal";
import styles from "./Cli.module.css";

const primary = [
  {
    cmd: "docker orbit generate",
    desc: "Read your docker-compose.yml and write an Orbit-enabled copy — the proxy is injected only for services that expose ports.",
  },
  {
    cmd: "docker orbit deploy",
    desc: "Bring the stack up behind the proxy, or reconcile it to the desired state.",
  },
  {
    cmd: "docker orbit rollout web",
    desc: "Roll a single service to a new version with a health-gated, zero-downtime swap.",
  },
];

const more = ["status", "rollback", "history", "doctor", "recover", "version"];

export default function Cli() {
  return (
    <section className="section" id="cli" aria-labelledby="cli-title">
      <div className="container">
        <p className="eyebrow">The CLI</p>
        <h2 id="cli-title" className="section-title">
          Three commands to go from Compose to zero-downtime.
        </h2>
        <p className="section-lead">
          Orbit installs as a native <code>docker</code> CLI plugin, so every
          command reads like Docker you already know.
        </p>

        <div className={styles.grid}>
          <ol className={styles.list}>
            {primary.map((c, i) => (
              <li key={c.cmd} className={styles.item}>
                <div className={styles.itemHead}>
                  <span className={styles.step}>{i + 1}</span>
                  <code className={styles.cmd}>{c.cmd}</code>
                </div>
                <p className={styles.desc}>{c.desc}</p>
              </li>
            ))}
          </ol>

          <div className={styles.aside}>
            <Terminal
              title="first deploy"
              lines={[
                { k: "cmd", t: "docker orbit generate" },
                { k: "ok", t: "✓ web    proxy injected" },
                { k: "warn", t: "⚠ db     skipped (database image)" },
                { k: "out", t: "→ wrote docker-rollout-compose.yml" },
                { k: "blank" },
                { k: "cmd", t: "docker orbit deploy" },
                { k: "ok", t: "✓ stack up · host port held by proxy" },
              ]}
            />

            <p className={styles.more}>
              Plus{" "}
              {more.map((m, i) => (
                <span key={m}>
                  <code>{m}</code>
                  {i < more.length - 1 ? ", " : " "}
                </span>
              ))}
              — see the{" "}
              <a href={site.links.cliReference} className={styles.moreLink}>
                CLI reference
              </a>
              .
            </p>
          </div>
        </div>
      </div>
    </section>
  );
}
