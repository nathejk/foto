CREATE TABLE IF NOT EXISTS photo (
    year VARCHAR(99) NOT NULL DEFAULT "",
    teamId VARCHAR(99) NOT NULL DEFAULT "",
    ref CHAR(64) NOT NULL,
    type VARCHAR(99) NOT NULL DEFAULT "",
    teamNumber VARCHAR(99) NOT NULL DEFAULT "",
    attention TINYINT(1) NOT NULL DEFAULT 0,
    contentType VARCHAR(99) NOT NULL DEFAULT "",
    bytes INT NOT NULL DEFAULT 0,
    width INT NOT NULL DEFAULT 0,
    height INT NOT NULL DEFAULT 0,
    -- The rendition set as JSON. A side table would add a join and a
    -- delete-on-replace path for no read this service makes: the set is small,
    -- always read with its photo, and written by exactly one event. If something
    -- ever needs to query across renditions -- "total thumbnail bytes this year"
    -- -- that is the moment to normalize it, and this column is a faithful record
    -- to migrate from.
    renditions TEXT NOT NULL,
    -- The smallest rendition, denormalized so the common read ("show me a
    -- thumbnail of this team") needs no JSON parsing.
    thumbRef CHAR(64) NOT NULL DEFAULT "",
    -- The original is recorded so a rendition set can be regenerated later. It is
    -- never served: it holds the upload's metadata, including any GPS the camera
    -- wrote. Serving happens from ref/thumbRef and the renditions only.
    originalRef CHAR(64) NOT NULL DEFAULT "",
    originalContentType VARCHAR(99) NOT NULL DEFAULT "",
    originalBytes INT NOT NULL DEFAULT 0,
    originalWidth INT NOT NULL DEFAULT 0,
    originalHeight INT NOT NULL DEFAULT 0,
    orientation INT NOT NULL DEFAULT 0,
    -- Where the bytes were fetched from, and when. The webhook carries a URL
    -- rather than bytes, so this is the only record of the photograph's origin.
    sourceUrl VARCHAR(999) NOT NULL DEFAULT "",
    sourceKind VARCHAR(99) NOT NULL DEFAULT "",
    capturedAt DATETIME NULL DEFAULT NULL,

    -- A team has many photographs, so the key cannot be (year, teamId) the way a
    -- person's single portrait can be. Identity is the content hash of the display
    -- image, which is what makes a replay converge: the same photograph
    -- re-delivered hashes to the same ref and rewrites one row, while a genuinely
    -- different photograph of the same team gets its own.
    --
    -- `type` is in the key so the same bytes filed as both "start" and "finish"
    -- remain two facts rather than one overwriting the other.
    PRIMARY KEY (year, teamId, type, ref),

    -- The read this service exists to serve: a team's photographs, newest first.
    KEY idx_photo_year_team (year, teamId, capturedAt),
    -- Finding a photograph by the number a human typed, for support questions
    -- ("team 42 says their picture is wrong").
    KEY idx_photo_year_number (year, teamNumber),
    -- Retention works from the clock, across all teams.
    KEY idx_photo_captured (capturedAt)
);
