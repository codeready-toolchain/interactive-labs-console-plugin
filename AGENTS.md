# Interactive labs Dynamic plugin

## Project description

This is an OpenShift Dynamic plugin for "interactive labs". An interactive lab is a set of instructions that a user can follow to achieve some goal in the OpenShift cluster.

A lab usually comes with a Helm chart that defines the resources that need to be present in the cluster prior to starting the lab.

The content of the lab is shown in an (minimizable and closeable) overlay panel.

The content is a collection of inter-linked Asciidoc pages (the Asciidoctor flavor). The pages can also contain special links pointing into the console itself. When the user clicks such a link, the corresponding section in the OpenShift console is highlighted similarly to how it is done in OpenShift quickstarts.

## Deployment and trust model

The plugin is installed in Red Hat customer OpenShift clusters. Red Hat authors and maintains lab repositories, so lab content is trusted.

Customers opt in to the labs available in their cluster by composing the plugin configuration ConfigMap with their selected labs. After a lab is enabled, the plugin backend keeps it up to date by polling its Red Hat-authored repository directly. Do not assume a separate CI validation or publishing environment for lab updates.

## Open questions

* How to handle the situation where more namespaces are required by the Helm chart?
* When only a single namespace is required by the helmchart, how to "pick" which one should it be?
* How to check that the user actually followed the instructions in the lab?
* How to "solve" a section of the lab for the user?
