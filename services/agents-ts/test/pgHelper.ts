import { randomUUID } from "node:crypto";
import pg from "pg";
import { migrate, PgAgentStore } from "../src/store/pg.js";

export const ownerUrl = process.env["PASSAGE_TEST_DATABASE_URL"];
const appUrl = process.env["PASSAGE_TEST_APP_DATABASE_URL"] ?? ownerUrl;

export interface Fixture {
  store: PgAgentStore;
  app: pg.Pool;
  owner: pg.Pool;
  close(): Promise<void>;
}

/** A private schema migrated by the owner role, used through the passage_app role when one is configured. */
export async function fixture(): Promise<Fixture> {
  const schema = `agents_${randomUUID().replaceAll("-", "").slice(0, 12)}`;
  const admin = new pg.Pool({ connectionString: ownerUrl, max: 1 });
  await admin.query(`create schema ${schema}`);
  const options = `-c search_path=${schema}`;
  const owner = new pg.Pool({ connectionString: ownerUrl, options, max: 2 });
  await migrate(owner);
  const app = new pg.Pool({ connectionString: appUrl, options, max: 16 });
  return {
    store: new PgAgentStore(app),
    app,
    owner,
    async close() {
      await app.end();
      await owner.end();
      await admin.query(`drop schema ${schema} cascade`);
      await admin.end();
    },
  };
}
