// Finance, controller and tenant-administration screens. Imported before ./shell, so the full ledger, legal entity,
// currency and fiscal calendar rows here win over the minimal ones the shell serves for the setup checklist.
import "./ledger";
import "./org";
import "./admin";
import "./compliance";
import "./assets";
import "./expense";
import "./finreport";
