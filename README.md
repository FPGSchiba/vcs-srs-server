# vngd-srs-server
Secret Project, psst, don't tell Dabble he won't understand the pain

## Better control Protocol (TCP)

With the new communication flow the UDP socket will only start, once the Client is sucessfully connected to the Server over TCP. This ensures a connection, that is verified by the Server. This also means the Server now manages all Clients and their respective IDs. A Client will no longer generate their own ID, but will get one from the Server. Without the ID a UDP Connection cannot be made.

### Old Message Types

| ID  | Type                             | Description                                               | Body     |
| --- | -------------------------------- | --------------------------------------------------------- | -------- |
| 0   | `UPDATE`                         | Meta Update with no Radio Information                     | `unkown` |
| 1   | `PING`                           | Send Ping                                                 | `unkown` |
| 2   | `SYNC`                           | Sync Meta and Radio Information                           | `unkown` |
| 3   | `RADIO_UPDATE`                   | Update Meta and Radio Information (Difference to `SYNC`?) | `unkown` |
| 4   | `SERVER_SETTINGS`                | Client requests Server Setttings from Server              | `unkown` |
| 5   | `CLIENT_DISCONNECT`              | Client disconnects completly (closing SRS)                | `unkown` |
| 6   | `VERSION_MISMATCH`               | Error message for mismatched protocol Version             | `unkown` |
| 7   | `EXTERNAL_AWACS_MODE_PASSWORD`   | Login request from Client                                 | `unkown` |
| 8   | `EXTERNAL_AWACS_MODE_DISCONNECT` | Disconnect only from AWACS                                | `unkown` |

### New Message Types

| ID  | Type               | Description                                                            | Body     |
| --- | ------------------ | ---------------------------------------------------------------------- | -------- |
| 0   | `UPDATE`           | Meta Update with no Radio Information                                  | `unkown` |
| 1   | `PING`             | Send Ping                                                              | `unkown` |
| 2   | `SYNC`             | Sync Meta and Radio Information                                        | `unkown` |
| 3   | `RADIO_UPDATE`     | Update Meta and Radio Information (Difference to `SYNC`?)              | `unkown` |
| 4   | `SERVER_SETTINGS`  | Client requests Server Setttings from Server                           | `unkown` |
| 5   | `DISCONNECT`       | Client disconnects completly (closing SRS)                             | `unkown` |
| 6   | `VERSION_MISMATCH` | Error message for mismatched protocol Version                          | `unkown` |
| 7   | `LOGIN`            | Login request from Client                                              | `unkown` |
| 8   | `LOGIN_SUCCESS`    | Sent to the clien to start the UDP Loop. (Only received by the Client) | `unkown` |
| 9   | `LOGIN_FAILED`     | Sent to the Client to restart Login. (Only received by the Client)     | `unkown` |

### Login Flow

$$
\begin{align}
    (Client) &\to [\text{\verb|LOGIN|}] &\to (Server) \\
    (Server) &\to 
    \begin{cases}
        [\text{\verb|LOGIN_SUCESS|}] &\text{if versionCheck and validPassword} \\
        [\text{\verb|LOGIN_FAILED|}] &\text{if !validPassword and versionCheck} \\
        [\text{\verb|VERSION_MISMATCH|}] &\text{if !versionCheck}
    \end{cases} &\to (Client) \\
\end{align}
$$

