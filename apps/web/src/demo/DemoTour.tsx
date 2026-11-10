import { Link } from "react-router-dom";
import { Card, Icon } from "../shell/ui";
import { TOUR } from "./tour";
import styles from "./demo.module.css";

/** Home page panel listing the demo's showcase screens, grouped by area. */
export function DemoTour() {
  const areas = [...new Set(TOUR.map((s) => s.area))];
  return (
    <section aria-labelledby="demo-tour-title" className={styles.tour}>
      <Card>
        <h2 id="demo-tour-title" className={styles.tourTitle}>
          Demo tour
        </h2>
        <p className={styles.tourIntro}>
          Cedar Ridge Pumps is a fictional manufacturer of industrial pumps. These screens have sample data; the rest of Passage opens, but most screens are empty in the demo.
        </p>
        <div className={styles.tourAreas}>
          {areas.map((area) => (
            <div key={area}>
              <h3 className={styles.tourArea}>{area}</h3>
              <ul className={styles.tourList}>
                {TOUR.filter((s) => s.area === area).map((s) => (
                  <li key={s.to}>
                    <Link to={s.to} className={styles.tourLink}>
                      <span className={styles.tourLabel}>
                        {s.label}
                        <Icon name="chevronRight" />
                      </span>
                      <span className={styles.tourBlurb}>{s.blurb}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </Card>
    </section>
  );
}
