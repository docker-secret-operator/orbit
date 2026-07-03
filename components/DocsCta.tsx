import { site } from "@/lib/site";
import { BookIcon, ArrowDownIcon, ExternalIcon } from "./icons";
import styles from "./DocsCta.module.css";

export default function DocsCta() {
  return (
    <section className="section" aria-labelledby="cta-title">
      <div className="container">
        <div className={styles.card}>
          <h2 id="cta-title" className={styles.title}>
            Give Compose the deploys it deserves.
          </h2>
          <p className={styles.lead}>
            Read how it works end to end, drop the binary next to your stack, or
            start from a working example.
          </p>
          <div className={styles.actions}>
            <a className="btn btn-primary" href={site.docs}>
              <BookIcon width={18} height={18} />
              Read documentation
            </a>
            <a className="btn" href="#install">
              <ArrowDownIcon width={18} height={18} />
              Install Orbit
            </a>
            <a
              className="btn btn-ghost"
              href={site.examples}
              target="_blank"
              rel="noreferrer"
            >
              <ExternalIcon width={18} height={18} />
              View examples
            </a>
          </div>
        </div>
      </div>
    </section>
  );
}
