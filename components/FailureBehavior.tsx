import { site } from "@/lib/site";
import { ExternalIcon } from "./icons";
import styles from "./FailureBehavior.module.css";

type Tone = "neutral" | "bad" | "good";
const flow: { t: string; s: string; tone: Tone }[] = [
  { t: "Deploy starts", s: "New backend scales up alongside the old one", tone: "neutral" },
  { t: "Health check fails", s: "New backend never registers with the proxy", tone: "bad" },
  { t: "New backend removed", s: "Scaled back down — no partial state left behind", tone: "neutral" },
  { t: "Old backend untouched", s: "It kept serving the whole time", tone: "good" },
  { t: "Deploy exits non-zero", s: "Traffic intact · nothing to roll back", tone: "good" },
];

const exitCodes = [
  { c: "0", m: "Success" },
  { c: "1", m: "Deploy ran but failed" },
  { c: "2", m: "Aborted before any change" },
  { c: "3", m: "Pre-flight checks failed" },
  { c: "4", m: "Succeeded, unhealthy backends remain" },
];

export default function FailureBehavior() {
  return (
    <section className="section" id="failure" aria-labelledby="failure-title">
      <div className="container">
        <p className="eyebrow">When a deploy goes wrong</p>
        <h2 id="failure-title" className="section-title">
          Orbit prefers availability over a successful deploy.
        </h2>
        <p className="section-lead">
          A new build only takes traffic after its health check passes. If it
          never does, the deploy fails — but your service does not. In every
          failure before the new backend is registered, the old one keeps
          serving.
        </p>

        <ol className={styles.flow}>
          {flow.map((step, i) => (
            <li key={i} className={`${styles.step} ${styles[step.tone]}`}>
              <span className={styles.node} aria-hidden>
                {step.tone === "bad" ? "×" : step.tone === "good" ? "✓" : i + 1}
              </span>
              <span className={styles.stepBody}>
                <span className={styles.stepTitle}>{step.t}</span>
                <span className={styles.stepSub}>{step.s}</span>
              </span>
            </li>
          ))}
        </ol>

        <div className={styles.grid}>
          <blockquote className={styles.property}>
            <p>
              “In every pre-registration failure, the old backend continues
              serving — traffic is never lost.”
            </p>
            <cite>
              Validated by failure-injection tests at each rollout step
            </cite>
          </blockquote>

          <div className={styles.codes}>
            <p className={styles.codesLabel}>
              <code>docker orbit deploy</code> exit codes
            </p>
            <ul>
              {exitCodes.map((e) => (
                <li key={e.c}>
                  <span className={styles.code}>{e.c}</span>
                  <span>{e.m}</span>
                </li>
              ))}
            </ul>
            <a className={styles.evidence} href={site.links.reliabilityReport}>
              Failure matrix &amp; recovery report
              <ExternalIcon width={15} height={15} />
            </a>
          </div>
        </div>
      </div>
    </section>
  );
}
