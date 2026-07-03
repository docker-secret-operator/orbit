import styles from "./ProblemTimeline.module.css";

type Step = { t: string; tone?: "bad" | "good" };

function Track({
  label,
  sub,
  steps,
  variant,
}: {
  label: string;
  sub: string;
  steps: Step[];
  variant: "bad" | "good";
}) {
  return (
    <div className={`${styles.track} ${styles[variant]}`}>
      <div className={styles.trackHead}>
        <span className={styles.trackLabel}>{label}</span>
        <span className={styles.trackSub}>{sub}</span>
      </div>
      <ol className={styles.steps}>
        {steps.map((s, i) => (
          <li
            key={i}
            className={`${styles.step} ${
              s.tone ? styles[`is-${s.tone}`] : ""
            }`}
          >
            <span className={styles.node} aria-hidden />
            <span className={styles.stepText}>{s.t}</span>
          </li>
        ))}
      </ol>
    </div>
  );
}

export default function ProblemTimeline() {
  return (
    <section className="section" id="why" aria-labelledby="why-title">
      <div className="container">
        <p className="eyebrow">Why Orbit exists</p>
        <h2 id="why-title" className="section-title">
          Compose recreates containers by stopping the old one first.
        </h2>
        <p className="section-lead">
          That gap is usually under a second. It is still long enough to drop
          in-flight HTTP requests, fail a load balancer health check, and cut
          every open WebSocket. Orbit removes the gap — nothing more, nothing
          less.
        </p>

        <div className={styles.grid}>
          <Track
            variant="bad"
            label="docker compose up --force-recreate"
            sub="the default"
            steps={[
              { t: "Old container stops" },
              { t: "Host port goes dark", tone: "bad" },
              { t: "In-flight requests fail", tone: "bad" },
              { t: "New container starts" },
              { t: "Health checks recover" },
              { t: "Users noticed the gap", tone: "bad" },
            ]}
          />
          <Track
            variant="good"
            label="docker orbit rollout web"
            sub="with Orbit"
            steps={[
              { t: "New container starts alongside" },
              { t: "Health check passes", tone: "good" },
              { t: "Proxy switches traffic", tone: "good" },
              { t: "Old container drains" },
              { t: "Old container removed" },
              { t: "Zero downtime", tone: "good" },
            ]}
          />
        </div>
      </div>
    </section>
  );
}
