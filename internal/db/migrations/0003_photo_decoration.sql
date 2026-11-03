-- How the owner dressed a photo: the frame and stickers are drawn by the app, so the image itself stays untouched.
ALTER TABLE photos ADD COLUMN decoration JSONB;
