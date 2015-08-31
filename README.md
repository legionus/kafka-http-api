## kafka-http-api

The project provides a High-level HTTP interface for Apache Kafka to store JSON messages.

### HTTP API

Url Structure: `{schema}://{host}/v1/topics/{topic}`  
Method: **POST**  
Description: Write message  


Url Structure: `{schema}://{host}/v1/topics/?queue={topic}`  
Method: **POST**  
Description: Write message (obsolete)  


### How to Install

    $ go get -u github.com/legionus/kafka-http-api

### LICENSE

The project is licensed under the GNU General Public License.

### Links

More about the Apache Kafka: http://kafka.apache.org/
