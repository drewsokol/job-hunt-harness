-- 0008: contacts beyond any one thread (docs/design.md, "Contacts").

-- Whether the contact's company is a recruiting agency or the employer.
ALTER TABLE contacts ADD COLUMN org_type TEXT CHECK (org_type IN ('agency', 'employer', 'other'));

-- How close the contact is: no real exchange yet, a real exchange, or
-- actively helping.
ALTER TABLE contacts ADD COLUMN warmth TEXT CHECK (warmth IN ('cold', 'warm', 'advocate'));

ALTER TABLE contacts ADD COLUMN phone TEXT;

-- Short lowercase words the agent groups contacts by.
ALTER TABLE contacts ADD COLUMN tags TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags));

-- When to get back in touch, for someone who said "I'll keep you in mind".
ALTER TABLE contacts ADD COLUMN re_engage_after TEXT;

CREATE INDEX contacts_re_engage ON contacts (re_engage_after) WHERE re_engage_after IS NOT NULL;
CREATE INDEX outreach_contact ON outreach (contact_id, sent_at);
