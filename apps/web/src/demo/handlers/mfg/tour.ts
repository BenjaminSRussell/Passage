import type { TourStop } from "../../tour";

// Manufacturing screens for the home page demo tour, in walkthrough order.
export const TOUR: TourStop[] = [
  { area: "Manufacturing", label: "Production orders", to: "/production", blurb: "Thirteen orders from scheduled to ended, with two running late. Open one to release or start it." },
  { area: "Manufacturing", label: "A late production order", to: "/production/MO-24618", blurb: "Impeller assemblies stuck behind the balancing bottleneck: components, route, costs and status log." },
  { area: "Manufacturing", label: "New production order", to: "/production/new", blurb: "Pick a pump by name, a site and a warehouse, add a route step and create the order; it lands in the production list." },
  { area: "Manufacturing", label: "Planned orders", to: "/planning/planned-orders", blurb: "What master planning proposes for the pumps and the short castings and motors. Tick a few and firm them." },
  { area: "Manufacturing", label: "Action messages", to: "/planning/messages", blurb: "Expedite, postpone and cancel suggestions against open purchase and production orders." },
  { area: "Manufacturing", label: "Scheduling Gantt", to: "/scheduling", blurb: "Work centres against time with firmed jobs and the late impeller balancing job in red." },
  { area: "Manufacturing", label: "Schedule runs", to: "/scheduling/runs", blurb: "Every scheduling run and saved Gantt move. Start a run and it joins the history. On the Gantt, choose Move on a job to preview and save a new start." },
  { area: "Manufacturing", label: "Capacity load", to: "/scheduling/load", blurb: "Impeller balancing is over 100% for weeks, the CNC cell is close behind." },
  { area: "Manufacturing", label: "Andon board", to: "/shopfloor/andon", blurb: "Open alerts on the floor right now: a balancer fault, rising scrap, a failed hydro test." },
  { area: "Manufacturing", label: "OEE", to: "/shopfloor/oee", blurb: "Availability, performance and quality across all three shifts, with the balancer's stoppage showing. Pick one machine for its live tile." },
  { area: "Manufacturing", label: "Quality orders", to: "/quality/orders", blurb: "Inspections from receiving and production, from just created to closed with a usage decision." },
  { area: "Manufacturing", label: "New quality order", to: "/quality/orders/new", blurb: "Raise a manual inspection by choosing the item, site and warehouse by name, then record its results." },
  { area: "Manufacturing", label: "Bills of material", to: "/engineering/boms", blurb: "Active, superseded and draft BOM versions for the pumps and their subassemblies." },
];
