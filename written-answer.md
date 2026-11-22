1. A service stores user names in users.display_name. A proposed release introduces users.full_name, changes all application reads and writes to the new column, and drops display_name in the same deployment. The service uses rolling deployment, so old and new application instances run concurrently. The release must also support rollback. Explain why this release can fail and propose a safe sequence of database and application changes.
ANSWER: 
If during upgrade, some bug is discovered, and must do a rollback, the logic in the app server will again use the previous users.display_name for service. But this column in database has already been changed.
Also another scenario is during a rolling upgrade, both the old and new version of the app server is up, therefore there will still be an old version of app server is up running, but the users.display_name is dropped, thus this will create a discrepency of the data read by the app server.
A safe sequence of database and application changes:
a. Add the new colume users.full_name to the database schema
b. Deploy a version of app that read users.display_name and write both users.full_name and users.display_name
c. After a full deployment, then do a database migration for users.display_name, for each record, copy users.display_name to null users.full_name
d. Deploy a new version of app server that read users.full_name and write both users.full_name and users.display_name
e. If everything works fine for the new version, then change the app server to read and write only users.full_name

2. Certificate details are cached for 60 seconds. Request A misses the cache and reads ACTIVE from the database. Before A writes to the cache, request B revokes the certificate, commits the database transaction, and deletes the cache entry. A then writes its stale ACTIVE value into the cache. A colleague proposes reducing the cache TTL to five seconds. Explain why this does not fix the problem. Redesign the read and revocation paths so that any read starting after B returns success cannot receive the stale ACTIVE value.
ANSWER: 
A shorter TTL only reduces how long the wrong value may remain in the cache, but the wrong value of ACTIVE will still be updated into the cache key, resulting in the staleness of cache.
The redesign should add a lock mechanism, for revoke workflow
a. Acquire the certificate's lock.
b. Update the database and write the audit event in one transaction.
c. Delete the cache entry, even if the certificate was already revoked.
d. Release the lock and return success.
for read workflow
a. Check the cache. If the value exists, return it.
b. If the cache is empty, acquire the certificate’s lock.
c. Read the current value from the database and fill the cache.
d. Release the lock and return the result.

3. Service A has 50 request workers. Every request to its /report endpoint calls Service B with a 100 ms timeout and up to three retries. Traffic to /report remains at 1,000 requests per second. B's latency suddenly rises from 20 ms to 2 seconds, and unrelated endpoints on A soon stop responding.Explain the chain of failure, including the effect of retries. Redesign /report so unrelated endpoints remain available, and state what /report should return when B is unhealthy.
ANSWER:
When B becomes slow, each attempt waits until the 100 ms timeout, and will have several times of retry. Each worker will take a much longer time for the processing of the request, this will result in a running out of the worker thread. So other request coming in later will have to wait for a free worker.
Redesign:
a. Use a circuit breaker to stop continuous when B keeps failing. And send a probe request to check if B recovers, also using a exponential backoff retry strategy, first retry after 2s, the next after 4s.
b. When B is unhealthy, /report should return 503 and let the user to try again later. 


This part is NOT written by AI.