-- Fixtures for the explicit-property-clear harness (092). One family, two
-- properties, one vehicle, an asset at each property and an asset at NO
-- property (so "the asset has no property, so there is nothing to derive" has
-- a case too). Mirrors subject_contradiction/fixtures.sql so the two harnesses
-- describe the same world.
INSERT INTO households (id, name) VALUES ('11111111-1111-1111-1111-111111111111','Probe')
  ON CONFLICT DO NOTHING;
INSERT INTO properties (id, household_id, name) VALUES
  ('22222222-2222-2222-2222-222222222222','11111111-1111-1111-1111-111111111111','House A'),
  ('44444444-4444-4444-4444-444444444444','11111111-1111-1111-1111-111111111111','House B')
  ON CONFLICT DO NOTHING;
INSERT INTO vehicles (id, household_id, make, model) VALUES
  ('55555555-5555-5555-5555-555555555555','11111111-1111-1111-1111-111111111111','Ford','Truck')
  ON CONFLICT DO NOTHING;
INSERT INTO assets (id, household_id, property_id, name) VALUES
  ('33333333-3333-3333-3333-333333333333','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','Furnace'),
  ('77777777-7777-7777-7777-777777777777','11111111-1111-1111-1111-111111111111','44444444-4444-4444-4444-444444444444','Boiler'),
  ('88888888-8888-8888-8888-888888888888','11111111-1111-1111-1111-111111111111',NULL,'Loose generator')
  ON CONFLICT DO NOTHING;
