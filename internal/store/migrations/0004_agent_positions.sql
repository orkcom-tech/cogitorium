-- Canvas coordinates for the blueprint editor. NULL means "not placed yet";
-- the UI lays those out automatically until the operator moves them.
ALTER TABLE agents ADD COLUMN pos_x REAL;
ALTER TABLE agents ADD COLUMN pos_y REAL;
