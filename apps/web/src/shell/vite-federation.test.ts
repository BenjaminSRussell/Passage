import { federationRemotes } from "./federation";

describe("module federation host contract", () => {
  it("declares the admin remote entry", () => {
    expect(federationRemotes.admin).toContain("remoteEntry.js");
  });
});
